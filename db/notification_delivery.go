package db

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"strings"
	"time"
)

// File ini berisi pengambilan tugas pengiriman dan transisi statusnya.
//
// Pengambilan memakai "lease" bukan transaksi panjang: baris diubah ke sending
// dan next_attempt_at didorong ke masa depan sebagai waktu habis lease, baru
// setelah transaksi di-commit barulah pengiriman jaringan dilakukan. Dengan
// begitu lock database tidak dipegang selama pengiriman, karena request jaringan
// bisa makan waktu beberapa detik (timeout klien 15 detik) dan memegang lock
// baris selama itu akan memperlambat operasi tulis lain di database yang sama.
//
// Konsekuensinya, jika proses crash di tengah pengiriman, baris akan tertahan
// di sending. Ini **bisa pulih sendiri**: setelah lease habis, next_attempt_at
// jatuh ke masa lalu, dan putaran pengambilan berikutnya akan menjemput baris
// yang sama lagi (lihat kondisi pengambilan pada state IN ('pending','sending')).
// Hitungan percobaan sudah ditambah 1 saat pengambilan, jadi crash tidak akan
// menyebabkan retry tanpa batas, setelah kesempatan MaxNotifyAttempts habis,
// baris jatuh ke failed menunggu penanganan manual.

// MaxNotifyAttempts adalah jumlah maksimum percobaan pengiriman (termasuk yang
// pertama). Didefinisikan di sini, bukan di engine pengiriman, karena ini
// adalah kebijakan milik state machine itu sendiri, sementara engine hanya
// pelaksananya.
const MaxNotifyAttempts = 3

// MaxDigestBatchSize adalah jumlah maksimum pengiriman yang bisa digabung
// dalam satu batch ringkasan.
//
// Alasan batas ini adalah resource: jika dalam satu siklus ringkasan ternyata
// terjaring puluhan ribu temuan (sangat mungkin, satu scan penuh saja bisa
// menghasilkan itu), tanpa batas atas pengambilan akan membaca semua baris ke
// memori, merender jadi satu pesan yang sangat panjang, lalu sebagian besar
// terpotong oleh batas panjang pesan di kanal, yang berarti memori terbuang
// sia-sia dan temuan yang terpotong itu **hilang tanpa jejak**. Dengan batas
// atas, bagian yang berlebih tetap tersimpan di database untuk jadi batch
// berikutnya dan otomatis terkirim di siklus selanjutnya, jadi tidak ada yang
// hilang.
//
// Angka 500 dipilih karena setelah dirender jadi pesan, ukurannya masih
// "cukup terbaca" dalam batas 4096 byte WeCom Work; angka yang lebih besar
// hanya akan membuat pemotongan terjadi lebih belakang saja.
const MaxDigestBatchSize = 500

// NotificationDelivery adalah satu tugas pengiriman, berisi konfigurasi kanal
// dan snapshot event yang diperlukan untuk rendering.
type NotificationDelivery struct {
	ID            int64           `json:"id"`
	EventID       int64           `json:"event_id"`
	ChannelID     int64           `json:"channel_id"`
	State         string          `json:"state"`
	Attempts      int             `json:"attempts"`
	NextAttemptAt time.Time       `json:"next_attempt_at"`
	LastError     string          `json:"last_error"`
	BatchID       *int64          `json:"batch_id,omitempty"`
	CreatedAt     time.Time       `json:"created_at"`
	SentAt        *time.Time      `json:"sent_at,omitempty"`
	Snapshot      json.RawMessage `json:"snapshot,omitempty"`
	// Konteks rendering yang dimuat lewat join, tidak masuk JSON (DTO dirakit
	// di layer server).
	Channel *NotificationChannel `json:"-"`
	// FindingID/EventKind dibawa dari event, dipakai daftar riwayat untuk
	// langsung melompat ke detail temuan.
	FindingID int64  `json:"finding_id,string"`
	EventKind string `json:"event_kind"`
	// ChannelName/ChannelKind adalah field redundan untuk tampilan daftar,
	// supaya frontend tidak perlu query kedua kalinya.
	ChannelName string `json:"channel_name"`
	ChannelKind string `json:"channel_kind"`
}

const notificationDeliveryCols = `d.id, d.event_id, d.channel_id, d.state, d.attempts, d.next_attempt_at,
       d.last_error, d.batch_id, d.created_at, d.sent_at`

// joinedDeliveryQuery adalah bentuk baca gabungan untuk baris pengiriman:
// pengiriman + snapshot event + konfigurasi kanal. Ketiganya wajib ada untuk
// merender satu pesan, memisahkan query jadi tiga round-trip.
const joinedDeliveryQuery = `SELECT ` + notificationDeliveryCols + `,
       e.snapshot, e.kind, e.finding_id,
       c.id, c.name, c.kind, c.enabled, c.config, c.mode, c.filter, c.rate_per_min
FROM notification_deliveries d
JOIN notification_events e ON e.id = d.event_id
JOIN notification_channels c ON c.id = d.channel_id`

func scanNotificationDelivery(sc interface{ Scan(...any) error }) (*NotificationDelivery, error) {
	var (
		dl        NotificationDelivery
		lastErr   sql.NullString
		batchID   sql.NullInt64
		sentAt    sql.NullTime
		snapshot  []byte
		eventKind string
		channel   NotificationChannel
		chEnabled bool
	)
	if err := sc.Scan(&dl.ID, &dl.EventID, &dl.ChannelID, &dl.State, &dl.Attempts, &dl.NextAttemptAt,
		&lastErr, &batchID, &dl.CreatedAt, &sentAt,
		&snapshot, &eventKind, &dl.FindingID,
		&channel.ID, &channel.Name, &channel.Kind, &chEnabled, &channel.Config, &channel.Mode, &channel.Filter, &channel.RatePerMin); err != nil {
		return nil, err
	}
	dl.LastError = lastErr.String
	if batchID.Valid {
		dl.BatchID = &batchID.Int64
	}
	if sentAt.Valid {
		dl.SentAt = &sentAt.Time
	}
	dl.Snapshot = json.RawMessage(snapshot)
	dl.EventKind = eventKind
	dl.ChannelName = channel.Name
	dl.ChannelKind = channel.Kind
	channel.Enabled = &chEnabled
	dl.Channel = &channel
	return &dl, nil
}

// claimQuery menggambarkan satu kali pengambilan: pertama pilih kandidat
// lewat sel lalu lock, kemudian ubah jadi sending dan perpanjang lease. Posisi
// lease di sel diisi pemanggil lewat placeholder $n dan parameternya sendiri.
type claimQuery struct {
	sql  string
	args []any
}

// ClaimRealtimeDeliveries mengambil sejumlah (maksimal limit) pengiriman
// realtime yang sudah jatuh tempo pada satu kanal.
//
// Pengambilan sengaja dilakukan per **satu kanal**, bukan "ambil satu batch
// global lalu pilih mana yang dikirim": gerbang rate limit dikelola per kanal
// di dalam engine pengiriman, dan rate limit hanya bisa menghindari pemakaian
// jatah retry kalau kita tahu dulu berapa yang masih boleh dikirim kanal ini
// di putaran ini, baru mengambil baris sejumlah itu. Kalau urutannya dibalik
// (ambil dulu baru dibuang), baris yang ditahan rate limit sudah kena hitungan
// attempts satu kali, sehingga jatah 3 kali akan habis murni karena menunggu
// dan akhirnya jatuh ke failed.
//
// Kondisinya mencakup "sending yang lease-nya sudah habis", itulah titik
// pulih dari crash. lease harus jauh lebih besar dari waktu terburuk satu
// kali pengiriman (timeout klien HTTP kanal 15 detik), kalau tidak baris yang
// sama bisa dikirim dua dispatcher sekaligus. Kanal yang sudah dinonaktifkan
// juga diblokir di sini: operasi nonaktifkan sudah menandai pengiriman yang
// ada sebagai skipped, ini jadi lapisan pengaman tambahan agar tidak ada yang
// lolos saat nonaktifkan dan pengambilan terjadi bersamaan.
func (d *DB) ClaimRealtimeDeliveries(ctx context.Context, channelID int64, limit int, lease time.Duration) ([]*NotificationDelivery, error) {
	if limit <= 0 {
		return nil, nil
	}
	return d.claimDeliveries(ctx, lease, claimQuery{
		sql: `SELECT dd.id FROM notification_deliveries dd
JOIN notification_channels c ON c.id = dd.channel_id
WHERE dd.channel_id = $1 AND dd.state IN ($2,$3) AND dd.next_attempt_at <= now()
  AND c.enabled AND c.mode = $4
ORDER BY dd.next_attempt_at, dd.id
FOR UPDATE OF dd SKIP LOCKED
LIMIT $5`,
		args: []any{channelID, NotifyStatePending, NotifyStateSending, NotifyModeRealtime, limit},
	}, nil)
}

// DigestBatchDue melaporkan apakah kanal ini sudah terkumpul satu batch yang
// jatuh tempo: ada pengiriman yang menunggu, dan **yang paling tua** sudah
// mencapai umur siklus ringkasan.
//
// Patokannya umur pengiriman paling tua, bukan jam dinding: dengan begitu
// kanal yang baru dibuat tidak langsung memuntahkan "ringkasan" berisi satu
// item hanya karena pas selaras dengan jam bulat, dan batch yang sudah lama
// menumpuk juga tidak perlu menunggu satu putaran lagi dengan percuma.
//
// Dipisah dari ClaimDigestBatch karena maknanya berbeda: fungsi ini hanya
// menjawab "apakah sudah waktunya kirim", sedangkan pengambilan harus
// mengambil **semua** baris yang menunggu di kanal itu (termasuk yang belum
// cukup umur), kalau tidak satu siklus akan terpecah jadi beberapa pesan dan
// ringkasan jadi kehilangan maknanya.
func (d *DB) DigestBatchDue(ctx context.Context, channelID int64, minAge time.Duration) (bool, error) {
	var due bool
	err := d.QueryRowContext(ctx, `SELECT EXISTS (
  SELECT 1 FROM notification_deliveries d
  JOIN notification_channels c ON c.id = d.channel_id
  WHERE d.channel_id = $1 AND d.state IN ($2,$3) AND c.enabled
  GROUP BY d.channel_id
  HAVING min(d.created_at) <= now() - make_interval(secs => $4)
)`, channelID, NotifyStatePending, NotifyStateSending, int64(minAge.Seconds())).Scan(&due)
	return due, err
}

// ClaimDigestBatch mengambil pengiriman yang sedang jatuh tempo di satu kanal
// sebagai satu batch ringkasan, maksimal MaxDigestBatchSize per batch.
//
// Semua pengiriman dalam satu batch berbagi batch_id, memakai id terkecil
// dalam kumpulan itu sebagai nomor batch (stabil, mudah dibaca, tidak perlu
// sequence tambahan). Saat retry, COALESCE dipakai untuk mempertahankan nomor
// batch asli, sehingga fakta "N item ini dikirim bersama" tetap berlaku
// setelah beberapa kali retry.
//
// Mengambil N baris teratas berdasarkan id menaik, bukan acak: pengiriman
// yang paling dulu dibuat paling dulu dikirim, sehingga saat ada penumpukan
// tidak terjadi starvation di mana "temuan baru dikirim lebih dulu, temuan
// lama selalu tertunda di belakang".
func (d *DB) ClaimDigestBatch(ctx context.Context, channelID int64, limit int, lease time.Duration) ([]*NotificationDelivery, error) {
	if limit <= 0 {
		return nil, nil
	}
	// limit adalah **batas atas memori**, pemanggil mengirim MaxDigestBatchSize;
	// di sini dijepit sekali lagi untuk mencegah pemanggil mengirim nilai yang
	// lebih besar.
	//
	// Sengaja tidak menerima "jatah rate limit" sebagai ukuran batch: satuan
	// rate limit adalah jumlah pesan, satu batch hanya mengirim satu pesan dan
	// memakai satu token yang dipotong oleh takeTokens di layer server,
	// berbeda dimensi dengan "berapa temuan dimuat dalam satu batch". Pernah
	// dicoba memasukkan jatah request per putaran sebagai ukuran batch agar
	// rate_per_min berlaku untuk digest, hasilnya kanal dengan rate=20/menit
	// hanya memuat 1 temuan per batch, dan digest berubah jadi push realtime
	// yang cuma berbungkus teks ringkasan. Kalau perlu mengubah rate limit,
	// ubah want di takeTokens, jangan di sini.
	if limit > MaxDigestBatchSize {
		limit = MaxDigestBatchSize
	}
	out, err := d.claimDeliveries(ctx, lease, claimQuery{
		sql: `SELECT dd.id FROM notification_deliveries dd
JOIN notification_channels c ON c.id = dd.channel_id
WHERE dd.channel_id = $1 AND dd.state IN ($2,$3) AND dd.next_attempt_at <= now() AND c.enabled
ORDER BY dd.id
FOR UPDATE OF dd SKIP LOCKED
LIMIT $4`,
		args: []any{channelID, NotifyStatePending, NotifyStateSending, limit},
	}, func(tx *sql.Tx, ids []int64) error {
		batchID := ids[0]
		for _, id := range ids {
			if id < batchID {
				batchID = id
			}
		}
		ph, idArgs := placeholders(2, ids)
		_, err := tx.ExecContext(ctx, `UPDATE notification_deliveries SET batch_id = COALESCE(batch_id, $1)
WHERE id IN (`+ph+`)`, append([]any{batchID}, idArgs...)...)
		return err
	})
	return out, err
}

// claimDeliveries menjalankan "pilih + ubah ke sending sambil perpanjang
// lease + baca baris lengkap", semuanya dalam satu transaksi. postClaim
// adalah langkah tambahan opsional (dipakai batch ringkasan untuk menulis
// batch_id).
func (d *DB) claimDeliveries(ctx context.Context, lease time.Duration, cq claimQuery, postClaim func(*sql.Tx, []int64) error) ([]*NotificationDelivery, error) {
	tx, err := d.BeginTx(ctx, nil)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback() //nolint:errcheck // jadi no-op setelah commit berhasil

	ids, err := selectForClaim(ctx, tx, cq.sql, cq.args...)
	if err != nil {
		return nil, err
	}
	if len(ids) == 0 {
		return nil, tx.Commit()
	}
	// Ubah ke sending dan dorong next_attempt_at ke masa depan: momen masa
	// depan itu adalah waktu habis lease, sehingga "lease belum habis" dan
	// "belum waktunya retry" berbagi ekspresi kondisi yang sama, tanpa perlu
	// kolom baru.
	ph, idArgs := placeholders(3, ids)
	if _, err := tx.ExecContext(ctx, `UPDATE notification_deliveries
SET state=$1, attempts=attempts+1, next_attempt_at=now()+make_interval(secs => $2)
WHERE id IN (`+ph+`)`,
		append([]any{NotifyStateSending, lease.Seconds()}, idArgs...)...); err != nil {
		return nil, err
	}
	if postClaim != nil {
		if err := postClaim(tx, ids); err != nil {
			return nil, err
		}
	}
	out, err := loadDeliveriesTx(ctx, tx, ids)
	if err != nil {
		return nil, err
	}
	return out, tx.Commit()
}

func selectForClaim(ctx context.Context, tx *sql.Tx, query string, args ...any) ([]int64, error) {
	rows, err := tx.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var ids []int64
	for rows.Next() {
		var id int64
		if err := rows.Scan(&id); err != nil {
			return nil, err
		}
		ids = append(ids, id)
	}
	return ids, rows.Err()
}

func loadDeliveriesTx(ctx context.Context, tx *sql.Tx, ids []int64) ([]*NotificationDelivery, error) {
	ph, args := placeholders(1, ids)
	rows, err := tx.QueryContext(ctx, joinedDeliveryQuery+` WHERE d.id IN (`+ph+`) ORDER BY d.id`, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []*NotificationDelivery{}
	for rows.Next() {
		dl, err := scanNotificationDelivery(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, dl)
	}
	return out, rows.Err()
}

// MarkDeliveriesSent menandai sekumpulan pengiriman sebagai sudah terkirim.
func (d *DB) MarkDeliveriesSent(ctx context.Context, ids []int64) error {
	ph, args := placeholders(2, ids)
	if len(args) == 0 {
		return nil
	}
	_, err := d.ExecContext(ctx, `UPDATE notification_deliveries
SET state=$1, sent_at=now(), last_error='' WHERE id IN (`+ph+`)`, append([]any{NotifyStateSent}, args...)...)
	return err
}

// RescheduleDeliveries mengembalikan sekumpulan pengiriman ke pending dan
// menunda waktu retry-nya.
//
// Dikembalikan ke pending, bukan membuat state antara baru, agar "berapa
// kesempatan yang masih tersisa" hanya diekspresikan di satu tempat
// (MaxNotifyAttempts), supaya cabang state machine tidak membengkak seiring
// perubahan kebijakan retry.
func (d *DB) RescheduleDeliveries(ctx context.Context, ids []int64, delay time.Duration, errMsg string) error {
	ph, args := placeholders(4, ids)
	if len(args) == 0 {
		return nil
	}
	_, err := d.ExecContext(ctx, `UPDATE notification_deliveries
SET state=$1, next_attempt_at=now()+make_interval(secs => $2), last_error=$3
WHERE id IN (`+ph+`)`,
		append([]any{NotifyStatePending, delay.Seconds(), truncateNotifyError(errMsg)}, args...)...)
	return err
}

// DeferDeliveries mengembalikan sekumpulan pengiriman ke pending agar bisa
// langsung diambil lagi, dan **membatalkan satu hitungan percobaan yang
// sudah ditambahkan saat pengambilan**.
//
// Kegunaannya hanya satu: saat pesan ringkasan dipecah sesuai batas panjang
// kanal, item yang tidak termuat di bagian ini harus disisakan untuk batch
// berikutnya. Itu bukan kegagalan, jadi tidak boleh memakan jatah retry,
// attempts sudah ditambah 1 secara optimis saat pengambilan, di sini harus
// dikurangi kembali. Kalau tidak, penumpukan 500 item yang dipecah per 20
// item akan jadi 25 bagian, dan item di bagian belakang akan dinyatakan
// failed oleh MaxNotifyAttempts di bagian ke-3, padahal item itu belum
// pernah gagal sama sekali.
//
// GREATEST(...,0) menjaga kasus "seseorang memicu kirim ulang manual yang
// me-reset attempts ke nol lalu jalur ini dilewati lagi", agar hitungan
// tidak jadi negatif.
func (d *DB) DeferDeliveries(ctx context.Context, ids []int64, reason string) error {
	ph, args := placeholders(3, ids)
	if len(args) == 0 {
		return nil
	}
	_, err := d.ExecContext(ctx, `UPDATE notification_deliveries
SET state=$1, attempts=GREATEST(attempts-1, 0), next_attempt_at=now(), last_error=$2
WHERE id IN (`+ph+`)`,
		append([]any{NotifyStatePending, truncateNotifyError(reason)}, args...)...)
	return err
}

// FailDeliveries menandai sekumpulan pengiriman sebagai gagal final, menunggu
// dikirim ulang secara manual dari riwayat pengiriman.
func (d *DB) FailDeliveries(ctx context.Context, ids []int64, errMsg string) error {
	// Placeholder dimulai dari $3: $1 adalah state, $2 adalah last_error.
	ph, args := placeholders(3, ids)
	if len(args) == 0 {
		return nil
	}
	_, err := d.ExecContext(ctx, `UPDATE notification_deliveries SET state=$1, last_error=$2 WHERE id IN (`+ph+`)`,
		append([]any{NotifyStateFailed, truncateNotifyError(errMsg)}, args...)...)
	return err
}

// RetryNotificationDelivery mengirim ulang satu pengiriman secara manual:
// reset ke pending, nol-kan hitungan percobaan, dan langsung jatuh tempo.
// Mengosongkan hitungan ini sengaja dilakukan, saat seseorang menekan "kirim
// ulang" artinya penyebab kegagalan sebelumnya sudah ditangani, jadi tidak
// masuk akal membatasinya dengan hitungan lama.
func (d *DB) RetryNotificationDelivery(ctx context.Context, id int64) error {
	res, err := d.ExecContext(ctx, `UPDATE notification_deliveries
SET state=$2, attempts=0, next_attempt_at=now(), last_error=''
WHERE id=$1 AND state IN ($3,$4)`, id, NotifyStatePending, NotifyStateFailed, NotifyStateSkipped)
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return fmt.Errorf("pengiriman %d tidak ditemukan atau statusnya saat ini tidak mengizinkan kirim ulang", id)
	}
	return nil
}

// NotificationDeliveryFilter adalah kondisi pencarian untuk riwayat pengiriman.
type NotificationDeliveryFilter struct {
	ChannelID int64
	State     string
	EventKind string
}

func (f NotificationDeliveryFilter) where() (string, []any) {
	var conds []string
	var args []any
	if f.ChannelID > 0 {
		args = append(args, f.ChannelID)
		conds = append(conds, fmt.Sprintf("d.channel_id=$%d", len(args)))
	}
	if f.State != "" {
		args = append(args, f.State)
		conds = append(conds, fmt.Sprintf("d.state=$%d", len(args)))
	}
	if f.EventKind != "" {
		args = append(args, f.EventKind)
		conds = append(conds, fmt.Sprintf("e.kind=$%d", len(args)))
	}
	if len(conds) == 0 {
		return "", nil
	}
	return " WHERE " + strings.Join(conds, " AND "), args
}

// ListNotificationDeliveries mengembalikan riwayat pengiriman berhalaman,
// yang terbaru di depan.
func (d *DB) ListNotificationDeliveries(ctx context.Context, f NotificationDeliveryFilter, page, pageSize int) ([]*NotificationDelivery, int, error) {
	if page < 1 {
		page = 1
	}
	if pageSize <= 0 || pageSize > 200 {
		pageSize = 50
	}
	where, args := f.where()

	var total int
	if err := d.QueryRowContext(ctx, `SELECT count(*) FROM notification_deliveries d
JOIN notification_events e ON e.id = d.event_id`+where, args...).Scan(&total); err != nil {
		return nil, 0, err
	}

	q := fmt.Sprintf("%s%s ORDER BY d.id DESC LIMIT $%d OFFSET $%d",
		joinedDeliveryQuery, where, len(args)+1, len(args)+2)
	rows, err := d.QueryContext(ctx, q, append(args, pageSize, (page-1)*pageSize)...)
	if err != nil {
		return nil, 0, err
	}
	defer rows.Close()
	out := []*NotificationDelivery{}
	for rows.Next() {
		dl, err := scanNotificationDelivery(rows)
		if err != nil {
			return nil, 0, err
		}
		out = append(out, dl)
	}
	return out, total, rows.Err()
}

// truncateNotifyError memotong pesan error ke panjang yang diterima kolom.
// Respons dari kanal bisa sangat panjang (terutama webhook generik ke
// layanan buatan sendiri), tanpa pemotongan payload daftar riwayat akan
// membengkak.
func truncateNotifyError(msg string) string {
	const max = 500
	if len(msg) <= max {
		return msg
	}
	// Mundur ke batas karakter terdekat, agar tidak menyisakan setengah
	// karakter UTF-8 yang bisa tampil jadi karakter rusak di frontend.
	cut := max
	for cut > 0 && !isUTF8Start(msg[cut]) {
		cut--
	}
	return msg[:cut] + "…"
}

func isUTF8Start(b byte) bool { return b&0xC0 != 0x80 }

// placeholders menghasilkan string placeholder $n mulai dari start beserta
// argumennya, dipakai untuk IN (...). Contoh: start=3, ids=[7,8] -> "$3,$4", [7,8].
func placeholders(start int, ids []int64) (string, []any) {
	ph := make([]string, 0, len(ids))
	args := make([]any, 0, len(ids))
	for i, id := range ids {
		ph = append(ph, fmt.Sprintf("$%d", start+i))
		args = append(args, id)
	}
	return strings.Join(ph, ","), args
}
