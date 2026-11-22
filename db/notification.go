package db

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"strings"
	"time"

	"github.com/Autumn-27/artex/notify"
)

// File ini adalah lapisan konfigurasi channel dan event untuk pengiriman notifikasi IM. Pengambilan dan perubahan status tugas pengiriman ada di
// db/notification_delivery.go.
//
// Dua invarian yang harus dijaga saat mengubah file ini:
//
//  1. Transaksi penulisan temuan (RecordFindingTx) hanya memanggil InsertNotificationEventTx untuk melakukan satu insert tanpa validasi,
//     tidak membaca tabel terkait notifikasi apa pun, dan tidak melakukan pencocokan filter. Operasi baca yang ditambahkan di sini bisa
//     mencemari atau bahkan membatalkan transaksi penulisan temuan akibat kondisi filter yang salah dikonfigurasi pengguna.
//  2. Pencocokan filter tidak pernah menghasilkan error: konfigurasi yang cacat selalu diperlakukan sebagai "cocok" (lihat notify.Match). Lebih baik
//     mengirim berlebih daripada tidak terkirim.

// ErrNotificationChannelNotFound menandakan channel tidak ditemukan.
var ErrNotificationChannelNotFound = errors.New("channel notifikasi tidak ditemukan")

// Status pengiriman.
const (
	NotifyStatePending = "pending" // menunggu dikirim
	NotifyStateSending = "sending" // sudah diambil oleh salah satu dispatcher, lease belum kedaluwarsa
	NotifyStateSent    = "sent"    // sudah terkirim
	NotifyStateFailed  = "failed"  // percobaan ulang habis atau gagal permanen, bisa dikirim ulang manual
	NotifyStateSkipped = "skipped" // channel sudah dinonaktifkan, tidak dikirim lagi
)

// Mode pengiriman.
const (
	NotifyModeRealtime = "realtime"
	NotifyModeDigest   = "digest"
)

// ValidNotifyMode memvalidasi mode pengiriman terhadap daftar yang diizinkan (sama seperti findings.status: tidak memakai DB CHECK
// agar mudah diperluas nanti).
func ValidNotifyMode(m string) bool {
	return m == NotifyModeRealtime || m == NotifyModeDigest
}

// NotificationChannel adalah konfigurasi satu instance channel. Config dan Filter disimpan sebagai JSON mentah,
// parsing-nya diserahkan ke paket notify, lapisan db tidak memahami arti field-field tersebut.
type NotificationChannel struct {
	ID     int64           `json:"id"`
	Name   string          `json:"name"`
	Kind   string          `json:"kind"`
	Mode   string          `json:"mode"`
	Config json.RawMessage `json:"config"`
	Filter json.RawMessage `json:"filter"`
	// Enabled menggunakan pointer untuk membedakan antara "field tidak dikirim" dan "dikirim false secara eksplisit",
	// karena kontrol toggle di frontend hanya mengirim field yang memang diubah.
	Enabled    *bool     `json:"enabled,omitempty"`
	RatePerMin int       `json:"rate_per_min"`
	CreatedAt  time.Time `json:"created_at"`
	UpdatedAt  time.Time `json:"updated_at"`
}

// IsEnabled mengembalikan status aktif channel; jika Enabled nil (belum dimuat) dianggap aktif.
func (c *NotificationChannel) IsEnabled() bool { return c.Enabled == nil || *c.Enabled }

// NotificationEvent merepresentasikan satu kejadian event.
type NotificationEvent struct {
	ID        int64           `json:"id"`
	Kind      string          `json:"kind"`
	FindingID int64           `json:"finding_id"`
	Snapshot  json.RawMessage `json:"snapshot"`
	CreatedAt time.Time       `json:"created_at"`
}

const notificationChannelCols = `id, name, kind, enabled, config, mode, filter, rate_per_min, created_at, updated_at`

func scanNotificationChannel(sc interface{ Scan(...any) error }) (*NotificationChannel, error) {
	var c NotificationChannel
	var enabled bool
	if err := sc.Scan(&c.ID, &c.Name, &c.Kind, &enabled, &c.Config, &c.Mode, &c.Filter, &c.RatePerMin, &c.CreatedAt, &c.UpdatedAt); err != nil {
		return nil, err
	}
	c.Enabled = &enabled
	return &c, nil
}

// ListNotificationChannels mengembalikan semua instance channel, yang aktif ditampilkan lebih dulu, lalu diurutkan berdasarkan id.
// Pengurutan dilakukan di SQL agar UI dan dispatcher melihat urutan yang sama dan stabil.
func (d *DB) ListNotificationChannels(ctx context.Context) ([]*NotificationChannel, error) {
	rows, err := d.QueryContext(ctx, `SELECT `+notificationChannelCols+` FROM notification_channels
ORDER BY enabled DESC, id`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []*NotificationChannel{}
	for rows.Next() {
		c, err := scanNotificationChannel(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, c)
	}
	return out, rows.Err()
}

// NotificationChannelByID mengambil satu channel.
func (d *DB) NotificationChannelByID(ctx context.Context, id int64) (*NotificationChannel, error) {
	row := d.QueryRowContext(ctx, `SELECT `+notificationChannelCols+` FROM notification_channels WHERE id=$1`, id)
	c, err := scanNotificationChannel(row)
	if err == sql.ErrNoRows {
		return nil, ErrNotificationChannelNotFound
	}
	return c, err
}

// SaveNotificationChannel membuat atau memperbarui satu channel.
//
// Saat update, hanya field yang dikirim pemanggil secara eksplisit (tidak nil / tidak kosong) yang ditimpa, sehingga frontend bisa
// mengirim form drawer yang hanya berisi perubahan parsial tanpa perlu mengirim balik field di config yang tidak ditampilkan,
// karena mengirim balik field tersebut justru bisa menyebabkan "nilai bertopeng menimpa kunci rahasia asli".
func (d *DB) SaveNotificationChannel(ctx context.Context, c *NotificationChannel) (int64, error) {
	if c.Mode == "" {
		c.Mode = NotifyModeRealtime
	}
	// Di sini sengaja **tidak** melakukan pemrosesan apa pun pada nilai 0: 0 adalah konfigurasi sah yang berarti "tanpa batas laju".
	//
	// Pernah ditulis `if c.RatePerMin <= 0 { c.RatePerMin = nilaiDefault }` dengan maksud "beri default aman saat tidak
	// ditentukan", tapi itu juga ikut menelan kasus "diset 0 secara eksplisit". Dokumentasi, petunjuk UI, dan
	// takeTokens semuanya menafsirkan 0 sebagai tanpa batas laju, hanya di sini yang diam-diam mengubahnya jadi 20 (DingTalk/WeCom/Telegram)
	// atau 100 (Feishu), sehingga operator mengira sudah membuka batas laju padahal sebenarnya masih dibatasi 20/menit tanpa ada peringatan.
	//
	// Perbedaan antara "tidak ditentukan" dan "diset 0 secara eksplisit" hanya diketahui oleh pemanggil (field tidak ada di body
	// permintaan vs dikirim 0 secara jelas), sehingga nilai default diisi oleh lapisan server saat field tidak ada, lihat notifyCreateChannel.
	if c.RatePerMin < 0 {
		return 0, errors.New("nilai batas laju tidak boleh negatif")
	}
	if c.Config == nil {
		c.Config = json.RawMessage(`{}`)
	}
	if c.Filter == nil {
		c.Filter = json.RawMessage(`{}`)
	}
	enabled := c.IsEnabled()

	if c.ID == 0 {
		var id int64
		err := d.QueryRowContext(ctx, `INSERT INTO notification_channels(name,kind,enabled,config,mode,filter,rate_per_min)
VALUES($1,$2,$3,$4,$5,$6,$7) RETURNING id`,
			c.Name, c.Kind, enabled, string(c.Config), c.Mode, string(c.Filter), c.RatePerMin).Scan(&id)
		return id, err
	}
	res, err := d.ExecContext(ctx, `UPDATE notification_channels
SET name=$2, kind=$3, enabled=$4, config=$5, mode=$6, filter=$7, rate_per_min=$8
WHERE id=$1`,
		c.ID, c.Name, c.Kind, enabled, string(c.Config), c.Mode, string(c.Filter), c.RatePerMin)
	if err != nil {
		return 0, err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return 0, ErrNotificationChannelNotFound
	}
	return c.ID, nil
}

// SetNotificationChannelEnabled mengubah status aktif/nonaktif channel.
//
// Saat menonaktifkan sebuah channel, semua pengiriman yang belum terkirim ditandai skipped juga, agar saat diaktifkan
// kembali tidak muncul tiba-tiba sekumpulan temuan lama yang "menumpuk selama nonaktif", yang sudah tidak relevan dan mudah disalahartikan sebagai temuan baru.
func (d *DB) SetNotificationChannelEnabled(ctx context.Context, id int64, enabled bool) error {
	return d.WithEvidenceTx(ctx, func(tx *sql.Tx) error {
		res, err := tx.ExecContext(ctx, `UPDATE notification_channels SET enabled=$2 WHERE id=$1`, id, enabled)
		if err != nil {
			return err
		}
		if n, _ := res.RowsAffected(); n == 0 {
			return ErrNotificationChannelNotFound
		}
		if !enabled {
			if _, err := tx.ExecContext(ctx, `UPDATE notification_deliveries SET state=$2, last_error=$3
WHERE channel_id=$1 AND state IN ($4,$5)`,
				id, NotifyStateSkipped, "channel sudah dinonaktifkan", NotifyStatePending, NotifyStateSending); err != nil {
				return err
			}
		}
		return nil
	})
}

// DeleteNotificationChannel menghapus channel. Riwayat pengirimannya terhapus otomatis lewat cascade foreign key
// (karena konfigurasi channel sudah tidak ada, riwayatnya pun tidak bisa diinterpretasikan lagi).
func (d *DB) DeleteNotificationChannel(ctx context.Context, id int64) error {
	res, err := d.ExecContext(ctx, `DELETE FROM notification_channels WHERE id=$1`, id)
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return ErrNotificationChannelNotFound
	}
	return nil
}

// RecordNotificationEventTx melakukan **upaya terbaik** untuk menulis satu event notifikasi di dalam transaksi milik pemanggil.
//
// Ini adalah satu-satunya perubahan terkait notifikasi pada jalur penulisan temuan: satu INSERT, tidak membaca tabel apa pun, tidak mengenal channel,
// dan tidak menjalankan filter. Dengan commit transaksi, "temuan tersimpan" dan "tugas pengiriman ada" terjamin konsisten secara atomik,
// tidak ada celah di mana commit berhasil tapi event tidak masuk antrean dan pesan hilang permanen.
//
// Dua keputusan desain penting di sini bukan ditulis sembarangan:
//
//  1. **Mengapa pakai SAVEPOINT**: di PostgreSQL, jika satu statement dalam transaksi gagal, seluruh transaksi masuk ke status
//     aborted, dan setelah itu semua statement (termasuk COMMIT) pasti gagal. Jadi "mengabaikan error dari INSERT ini
//     dan membiarkan pemanggil tetap commit" tidak bisa dilakukan di PG, kecuali errornya diisolasi dengan savepoint
//     pada statement tersebut. Tanpa savepoint, satu-satunya pilihan yang tersisa adalah "rollback seluruh transaksi".
//
//  2. **Mengapa rollback seluruh transaksi itu salah**: pengiriman notifikasi adalah fitur pelengkap, sedangkan pencatatan temuan adalah
//     inti produk. Masalah pada tabel notifikasi (migrasi database lama belum jalan, gangguan disk sesaat) tidak boleh membuat
//     temuan berbahaya gagal tersimpan. Karena itu di sini errornya diisolasi, dicatat ke log, dan mengembalikan false, sehingga
//     penulisan temuan tetap bisa commit seperti biasa, dengan konsekuensi kehilangan satu event notifikasi ini.
//     Mengembalikan bool bukan error dilakukan dengan sengaja: pemanggil tidak seharusnya menganggap ini sebagai error yang memengaruhi keberhasilan penulisan.
func RecordNotificationEventTx(ctx context.Context, tx *sql.Tx, kind string, findingID int64, snap notify.Snapshot) bool {
	raw, err := json.Marshal(snap)
	if err != nil {
		log.Printf("[notify] gagal serialisasi event notifikasi finding=%d: %v", findingID, err)
		return false
	}
	if _, err := tx.ExecContext(ctx, `SAVEPOINT notify_event`); err != nil {
		log.Printf("[notify] gagal membuat savepoint finding=%d: %v", findingID, err)
		return false
	}
	if _, err := tx.ExecContext(ctx, `INSERT INTO notification_events(kind,finding_id,snapshot) VALUES($1,$2,$3)`,
		kind, findingID, string(raw)); err != nil {
		log.Printf("[notify] gagal menulis event notifikasi finding=%d (catatan temuan tidak terpengaruh): %v", findingID, err)
		// Rollback ke savepoint untuk menyelamatkan transaksi dari status aborted.
		if _, rbErr := tx.ExecContext(ctx, `ROLLBACK TO SAVEPOINT notify_event`); rbErr != nil {
			log.Printf("[notify] gagal rollback ke savepoint finding=%d: %v", findingID, rbErr)
		}
		return false
	}
	// Lepaskan savepoint agar tidak menumpuk savepoint yang tidak terpakai pada transaksi panjang.
	_, _ = tx.ExecContext(ctx, `RELEASE SAVEPOINT notify_event`)
	return true
}

// AddNotificationEvent adalah versi transaksi mandiri dari InsertNotificationEventTx, untuk dipakai oleh titik
// pemanggil yang tidak berada dalam transaksi yang sudah ada (misalnya "kirim pesan uji" pada channel, yang tidak punya finding sungguhan).
func (d *DB) AddNotificationEvent(ctx context.Context, kind string, findingID int64, snap notify.Snapshot) (int64, error) {
	raw, err := json.Marshal(snap)
	if err != nil {
		return 0, fmt.Errorf("gagal serialisasi snapshot event notifikasi: %w", err)
	}
	var id int64
	err = d.QueryRowContext(ctx, `INSERT INTO notification_events(kind,finding_id,snapshot) VALUES($1,$2,$3) RETURNING id`,
		kind, findingID, string(raw)).Scan(&id)
	return id, err
}

// FanOutPendingEvents memecah event temuan yang belum didistribusikan menjadi tugas pengiriman berdasarkan channel yang
// sedang aktif, dan mengembalikan jumlah event yang diproses serta jumlah pengiriman baru pada ronde ini.
//
// Seluruh operasi berada dalam satu transaksi: event diambil dengan FOR UPDATE SKIP LOCKED, sehingga beberapa proses yang
// berjalan bersamaan tetap mendapat baris yang berbeda-beda (pengambilan antrean arsip di proyek ini memakai teknik yang sama, lihat
// completeNextArchiveJob di db/task_archives.go).
//
// Pencocokan filter sengaja dilakukan di sisi Go, bukan di SQL: kondisi filter pada channel adalah sekumpulan field JSONB
// opsional, dan mengekspresikan enam kombinasi pencocokan dalam SQL akan membuat query sulit dirawat, sementara jumlah channel
// biasanya hanya "beberapa yang dikonfigurasi manual", sehingga memuat semuanya lalu membandingkannya satu per satu di memori
// lebih cepat dan lebih mudah diuji.
//
// Event yang tidak cocok dengan channel mana pun tetap ditandai fanned_out, karena jika tidak, event itu akan terus tertinggal di
// kumpulan yang menunggu distribusi dan dipindai ulang setiap tick.
func (d *DB) FanOutPendingEvents(ctx context.Context, limit int) (eventCount, deliveryCount int, err error) {
	if limit <= 0 {
		limit = 200
	}
	tx, err := d.BeginTx(ctx, nil)
	if err != nil {
		return 0, 0, err
	}
	defer tx.Rollback() //nolint:errcheck // menjadi no-op setelah commit berhasil

	channels, err := listEnabledNotificationChannelsTx(ctx, tx)
	if err != nil {
		return 0, 0, err
	}
	rows, err := tx.QueryContext(ctx, `SELECT id, kind, finding_id, snapshot FROM notification_events
WHERE NOT fanned_out ORDER BY id FOR UPDATE SKIP LOCKED LIMIT $1`, limit)
	if err != nil {
		return 0, 0, err
	}
	var (
		events      []NotificationEvent
		parsedSnaps []notify.Snapshot
	)
	for rows.Next() {
		var ev NotificationEvent
		if err := rows.Scan(&ev.ID, &ev.Kind, &ev.FindingID, &ev.Snapshot); err != nil {
			rows.Close()
			return 0, 0, err
		}
		var snap notify.Snapshot
		// Snapshot ini ditulis oleh kode kita sendiri sehingga seharusnya selalu bisa diparse; kegagalan parsing tidak menghentikan
		// proses pengiriman, tapi event ini akan dilewati oleh semua channel yang punya kondisi filter karena semua field-nya kosong.
		// Lebih baik kehilangan satu pengiriman daripada membuat satu baris bermasalah menyumbat seluruh antrean.
		_ = json.Unmarshal(ev.Snapshot, &snap)
		// Nilai kind mengikuti kolom baris, bukan yang ada di snapshot: salinan di snapshot hanya untuk tampilan dan bisa ditulis oleh versi lama.
		snap.Kind = ev.Kind
		events = append(events, ev)
		parsedSnaps = append(parsedSnaps, snap)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return 0, 0, err
	}
	if len(events) == 0 {
		return 0, 0, tx.Commit()
	}

	type pending struct {
		eventID   int64
		channelID int64
	}
	var toInsert []pending
	for i, snap := range parsedSnaps {
		for _, ch := range channels {
			if !notify.Match(notify.ParseFilter(ch.Filter), snap) {
				continue
			}
			toInsert = append(toInsert, pending{eventID: events[i].ID, channelID: ch.ID})
		}
	}
	if len(toInsert) > 0 {
		var (
			vals []string
			args []any
		)
		for _, p := range toInsert {
			vals = append(vals, fmt.Sprintf("($%d,$%d)", len(args)+1, len(args)+2))
			args = append(args, p.eventID, p.channelID)
		}
		if _, err := tx.ExecContext(ctx, `INSERT INTO notification_deliveries(event_id,channel_id) VALUES `+strings.Join(vals, ","), args...); err != nil {
			return 0, 0, err
		}
	}

	// Tandai event pada ronde ini sudah didistribusikan. Event yang tidak cocok dengan channel mana pun juga ditandai sekaligus (lihat komentar fungsi).
	ids := make([]string, 0, len(events))
	markArgs := make([]any, 0, len(events))
	for _, ev := range events {
		markArgs = append(markArgs, ev.ID)
		ids = append(ids, fmt.Sprintf("$%d", len(markArgs)))
	}
	if _, err := tx.ExecContext(ctx, `UPDATE notification_events SET fanned_out=true WHERE id IN (`+strings.Join(ids, ",")+`)`, markArgs...); err != nil {
		return 0, 0, err
	}
	return len(events), len(toInsert), tx.Commit()
}

// listEnabledNotificationChannelsTx mengambil channel yang aktif dalam transaksi. Jumlahnya sedikit,
// sehingga tidak perlu paginasi atau cache, karena cache akan menimbulkan masalah tambahan soal "kapan perubahan konfigurasi berlaku".
func listEnabledNotificationChannelsTx(ctx context.Context, tx *sql.Tx) ([]*NotificationChannel, error) {
	rows, err := tx.QueryContext(ctx, `SELECT id, name, kind, config, mode, filter, rate_per_min
FROM notification_channels WHERE enabled ORDER BY id`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []*NotificationChannel{}
	for rows.Next() {
		var c NotificationChannel
		if err := rows.Scan(&c.ID, &c.Name, &c.Kind, &c.Config, &c.Mode, &c.Filter, &c.RatePerMin); err != nil {
			return nil, err
		}
		out = append(out, &c)
	}
	return out, rows.Err()
}

// NotificationAssetNames mengubah id aset menjadi nama tampilan singkat untuk dipakai pada pesan notifikasi.
//
// Urutan hasil sama dengan urutan input, panjangnya bisa lebih kecil dari input (id yang tidak ada dilewati). Urutan input dipertahankan
// agar urutan aset pada pesan temuan yang sama tetap stabil di berbagai pengiriman, karena jika tidak, pesan hasil percobaan ulang
// yang urutan asetnya berubah bisa disalahartikan sebagai "aset yang berubah".
func (d *DB) NotificationAssetNames(ctx context.Context, ids []int64) ([]string, error) {
	if len(ids) == 0 {
		return nil, nil
	}
	ph, args := placeholders(1, ids)
	rows, err := d.QueryContext(ctx, `SELECT id, type, domain, ip, url, app_name, bundle_id FROM assets WHERE id IN (`+ph+`)`, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	labels := map[int64]string{}
	for rows.Next() {
		var (
			id                int64
			typ               string
			domain, ip, url   sql.NullString
			appName, bundleID sql.NullString
		)
		if err := rows.Scan(&id, &typ, &domain, &ip, &url, &appName, &bundleID); err != nil {
			return nil, err
		}
		labels[id] = assetDisplayName(typ, domain.String, ip.String, url.String, appName.String, bundleID.String)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	out := make([]string, 0, len(ids))
	seen := map[int64]bool{}
	for _, id := range ids {
		if seen[id] {
			continue
		}
		seen[id] = true
		if label, ok := labels[id]; ok && label != "" {
			out = append(out, label)
		}
	}
	return out, nil
}

// assetDisplayName memilih identitas paling mudah dikenali berdasarkan tipe aset.
// Jika tidak ada yang cocok, mengembalikan string kosong dan pemanggil yang menentukan cara menampilkan "aset yang namanya tidak didapat".
// Fungsi ini tidak mengada-adakan placeholder, karena noise seperti "Aset#42" bisa tercampur ke pesan notifikasi dan pembaca mengiranya domain asli.
func assetDisplayName(typ, domain, ip, url, appName, bundleID string) string {
	pick := func(vals ...string) string {
		for _, v := range vals {
			if strings.TrimSpace(v) != "" {
				return v
			}
		}
		return ""
	}
	switch typ {
	case "root_domain", "subdomain":
		return domain
	case "ip":
		return ip
	case "app":
		return pick(appName, bundleID)
	case "service", "endpoint":
		return pick(url, domain, ip)
	default:
		return pick(domain, ip, url, appName)
	}
}

// SetFindingStatusWithNotify memperbarui status penanganan temuan, dan dalam transaksi yang sama mencatat satu event
// notifikasi perubahan status.
//
// Mengembalikan from=status sebelum perubahan; found=apakah temuan ada; notified=apakah event berhasil dicatat.
//
// Tiga perilaku yang disengaja:
//   - Event tidak dicatat jika status sebenarnya tidak berubah. Pengiriman ulang nilai yang sama dari drawer frontend, atau
//     replay idempoten dari skrip otomatis, tidak seharusnya menghasilkan noise notifikasi.
//   - Jika temuan tidak ada, mengembalikan found=false tanpa melakukan penulisan apa pun, dan pemanggil yang menerjemahkannya menjadi 404.
//   - Kegagalan mencatat event tidak mempengaruhi pembaruan status (lihat penjelasan savepoint di RecordNotificationEventTx),
//     sehingga saat notified=false status sudah berhasil diubah dan pemanggil tidak seharusnya menganggapnya error.
func (d *DB) SetFindingStatusWithNotify(ctx context.Context, id int64, status string) (from string, found bool, notified bool, err error) {
	err = d.WithEvidenceTx(ctx, func(tx *sql.Tx) error {
		var txErr error
		from, found, _, notified, txErr = SetFindingStatusTx(ctx, tx, id, status)
		return txErr
	})
	return from, found, notified, err
}

// SetFindingStatusTx memperbarui status temuan dan mencatat event notifikasi perubahan status di dalam **transaksi milik pemanggil**.
//
// Dibuat menjadi fungsi level transaksi agar semua jalur yang mengubah status memakai semantik yang sama. Sebelumnya hanya
// patchFinding yang memakai versi dengan notifikasi, sementara **saat kesimpulan retest adalah "sudah diperbaiki"** (statement
// `UPDATE findings SET status=...` di finding_retests) ditulis langsung ke database, sehingga channel yang mengonfigurasi
// `on_status_change` sama sekali tidak menerima notifikasi untuk perubahan status jenis ini: status berubah diam-diam di
// tampilan, dan tim operasional baru menyadarinya setelah membuka platform.
//
// Mengembalikan from=status sebelum perubahan, found=apakah temuan ada, changed=apakah status benar-benar berubah,
// notified=apakah event berhasil dicatat (kegagalan mencatat tidak mempengaruhi pembaruan status, lihat RecordNotificationEventTx).
func SetFindingStatusTx(ctx context.Context, tx *sql.Tx, id int64, status string) (from string, found bool, changed bool, notified bool, err error) {
	var (
		vulnclass, name, severity, summary string
		taskID                             sql.NullInt64
		assetIDs                           []byte
	)
	scanErr := tx.QueryRowContext(ctx, `SELECT vulnclass, name, severity, summary, task_id, asset_ids, status
FROM findings WHERE id=$1 FOR UPDATE`, id).
		Scan(&vulnclass, &name, &severity, &summary, &taskID, &assetIDs, &from)
	if scanErr == sql.ErrNoRows {
		return "", false, false, false, nil
	}
	if scanErr != nil {
		return "", false, false, false, scanErr
	}
	found = true
	if from == status {
		// Jika status tidak benar-benar berubah, event tidak dicatat: pengiriman ulang nilai yang sama atau replay idempoten
		// tidak seharusnya menghasilkan noise notifikasi.
		return from, true, false, false, nil
	}
	if _, err := tx.ExecContext(ctx, `UPDATE findings SET status=$2 WHERE id=$1`, id, status); err != nil {
		return from, true, false, false, err
	}
	var assets []int64
	_ = json.Unmarshal(assetIDs, &assets)
	notified = RecordNotificationEventTx(ctx, tx, notify.EventFindingStatusChanged, id, notify.Snapshot{
		Kind:       notify.EventFindingStatusChanged,
		FindingID:  id,
		TaskID:     taskID.Int64,
		VulnClass:  vulnclass,
		Name:       name,
		Severity:   severity,
		Summary:    summary,
		AssetIDs:   assets,
		FromStatus: from,
		ToStatus:   status,
	})
	return from, true, true, notified, nil
}

// NotificationStats adalah ringkasan hitungan di bagian atas halaman notifikasi.
type NotificationStats struct {
	Channels     int   `json:"channels"`
	ChannelsOn   int   `json:"channels_on"`
	Pending      int   `json:"pending"`
	Failed       int   `json:"failed"`
	SentToday    int   `json:"sent_today"`
	BacklogAgeMS int64 `json:"backlog_age_ms"` // usia dalam milidetik dari pengiriman tertunda yang paling lama
}

// NotificationStatsSnapshot merangkum kesehatan sistem notifikasi secara keseluruhan.
// BacklogAgeMS adalah indikator paling langsung untuk "apakah pengiriman tersendat", jauh lebih berguna dibanding sekadar
// menghitung jumlah pending, karena 3 antrean yang menumpuk bisa berarti rentang dari 3 detik sampai 3 jam.
func (d *DB) NotificationStatsSnapshot(ctx context.Context) (*NotificationStats, error) {
	var s NotificationStats
	if err := d.QueryRowContext(ctx, `SELECT
    (SELECT count(*) FROM notification_channels),
    (SELECT count(*) FROM notification_channels WHERE enabled),
    (SELECT count(*) FROM notification_deliveries WHERE state IN ($1,$2)),
    (SELECT count(*) FROM notification_deliveries WHERE state=$3),
    (SELECT count(*) FROM notification_deliveries WHERE state=$4 AND sent_at >= date_trunc('day', now())),
    COALESCE((SELECT EXTRACT(EPOCH FROM (now() - min(created_at))) * 1000 FROM notification_deliveries WHERE state=$1), 0)::bigint`,
		NotifyStatePending, NotifyStateSending, NotifyStateFailed, NotifyStateSent).
		Scan(&s.Channels, &s.ChannelsOn, &s.Pending, &s.Failed, &s.SentToday, &s.BacklogAgeMS); err != nil {
		return nil, err
	}
	return &s, nil
}
