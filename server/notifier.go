package server

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"sync"
	"time"

	"github.com/Autumn-27/artex/db"
	"github.com/Autumn-27/artex/notify"
)

// Kunci setting global (disimpan di tabel key-value settings, tidak perlu tabel baru).
const (
	// settingNotifyEnabled adalah saklar utama notifikasi. Default aktif, dipakai untuk menghentikan notifikasi sekali klik saat maintenance,
	// bukan syarat aktifnya fitur ini; syarat sebenarnya adalah "apakah ada channel yang dikonfigurasi".
	settingNotifyEnabled = "notify_enabled"
	// settingNotifyPublicBaseURL adalah alamat akses eksternal untuk membuat tautan balik
	// ke detail temuan (misalnya https://artex.example.com). Jika dikosongkan, pesan tidak menyertakan tombol tautan balik.
	// Belum ada konfigurasi alamat eksternal yang bisa dipakai ulang di project ini, jadi ditambahkan setting baru di sini.
	settingNotifyPublicBaseURL = "notify_public_base_url"
	// settingNotifyDigestMinutes adalah periode mode ringkasan (dalam menit).
	settingNotifyDigestMinutes = "notify_digest_interval_min"
)

const (
	// notifyTick adalah interval polling mesin pengiriman. 3 detik adalah batas atas
	// real-time mesin ini, dan juga sumber delay utama antara "temuan tersimpan di database" dan "pesan sampai di IM".
	notifyTick = 3 * time.Second
	// notifyLease adalah durasi lease saat mengambil tugas pengiriman. Harus jauh lebih besar dari waktu terburuk satu kali pengiriman
	// (timeout HTTP client paket notify adalah 15 detik), kalau tidak baris yang sama bisa dikirim
	// oleh dua dispatcher sekaligus.
	notifyLease = 3 * time.Minute
	// notifyFanOutPerTick membatasi jumlah event yang dibagikan per putaran, agar saat channel baru pertama kali diaktifkan,
	// seluruh backlog lama tidak langsung diubah semuanya menjadi tugas pengiriman sekaligus.
	notifyFanOutPerTick = 200
	// notifyDefaultDigestMinutes adalah nilai default periode ringkasan.
	notifyDefaultDigestMinutes = 30
	// notifyUnlimitedBurstPerTick adalah batas pengiriman per putaran saat channel tidak diberi rate limit.
	// Tujuannya mencegah "satu channel diset tanpa rate limit lalu satu kali scan menemukan ribuan temuan" membuat
	// satu putaran loop terblokir dalam waktu lama.
	notifyUnlimitedBurstPerTick = 50
	// notifyMaxSendsPerChannelPerTick adalah jumlah maksimum pengiriman per putaran untuk satu channel.
	//
	// Batas ini diturunkan dari **durasi lease**: saat diambil, setiap baris diberi lease (notifyLease = 3 menit),
	// jika jumlah pengiriman berurutan dalam satu putaran membuat waktu terburuk melebihi lease, baris terakhir belum selesai dikirim tapi lease-nya sudah habis.
	// Dalam satu proses tidak masalah (Run berjalan sebagai satu goroutine berurutan, tick tidak re-entrant), tapi kalau **dua
	// proses terhubung ke database yang sama**, proses lain akan mengambil ulang baris yang lease-nya sudah habis dan mengirim dua kali, serta
	// menambah attempts dua kali lipat, lalu menandai gagal padahal proses asal masih dalam proses mengirim.
	//
	// Nilai yang dipakai: lease 3 menit / timeout sekali kirim 30 detik = 6 adalah **tepat memakai seluruh lease** tanpa sisa,
	// tidak boleh dipakai; dipilih 5 agar waktu terburuk 150 detik menyisakan 30 detik. Hubungan ini dikunci oleh
	// TestNotifyTickBudgetFitsWithinLease, mengubah notifyLease,
	// notifySendTimeout, atau nilai ini akan membuat assertion itu gagal.
	notifyMaxSendsPerChannelPerTick = 5
	// notifySendTimeout adalah timeout untuk satu kali pengiriman. Nilai ini juga menentukan nilai konstanta sebelumnya,
	// hasil kali keduanya tidak boleh melebihi notifyLease, lihat TestNotifyTickBudgetFitsWithinLease.
	notifySendTimeout = 30 * time.Second
)

// notifyBackoff adalah urutan backoff untuk retry setelah gagal, indeksnya adalah jumlah percobaan yang sudah dilakukan.
// 3 kesempatan (termasuk percobaan pertama) harus sesuai dengan db.MaxNotifyAttempts, keduanya harus diubah bersamaan.
var notifyBackoff = []time.Duration{
	time.Second,
	5 * time.Second,
	30 * time.Second,
}

// Notifier adalah mesin pengiriman notifikasi temuan.
//
// Berjalan sejajar dengan Scheduler, sebagai goroutine independen (lihat server.New). Sengaja tidak memakai ulang
// tick milik Scheduler: kebutuhan real-time notifikasi (3 detik) berbeda dengan ritme bisnis trigger,
// dan kegagalan keduanya tidak saling menyeret; notifikasi yang macet tidak boleh mempengaruhi trigger agent.
type Notifier struct {
	s  *Server
	pg *db.DB

	// mu melindungi buckets. Jumlah channel sedikit dan kontensinya rendah, satu mutex sudah cukup,
	// tidak perlu struktur yang lebih granular.
	mu      sync.Mutex
	buckets map[int64]*notifyBucket
}

// notifyBucket adalah token bucket untuk satu channel.
//
// Memakai token bucket, bukan sliding window "hitung per menit lalu reset ke nol", karena efek batas window yang terakhir sangat buruk:
// mengirim penuh 20 pesan di akhir window, lalu 20 pesan lagi sesaat setelahnya, dari sisi platform itu jadi 40 pesan dalam satu detik,
// dan akan terkena rate limit; token bucket mengisi ulang dengan rate konstan, sehingga secara alami menghindari lonjakan seperti ini.
type notifyBucket struct {
	tokens   float64
	lastFill time.Time
}

func newNotifier(s *Server) *Notifier {
	return &Notifier{s: s, pg: s.m.pg, buckets: map[int64]*notifyBucket{}}
}

// Run berjalan dalam loop sampai ctx berakhir. Dijalankan sekali oleh server.New.
func (n *Notifier) Run(ctx context.Context) {
	if n.pg == nil {
		return
	}
	t := time.NewTicker(notifyTick)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			n.step(ctx)
		}
	}
}

// step menjalankan satu putaran: pertama membagikan event baru, lalu mengirim tugas yang sudah waktunya.
//
// Kegagalan di langkah manapun hanya dicatat ke log dan tidak menghentikan loop; kegagalan sistem notifikasi tidak boleh meningkat jadi masalah di level proses.
// Setiap tick berdiri sendiri, putaran berikutnya akan otomatis mencoba ulang.
func (n *Notifier) step(ctx context.Context) {
	if !n.enabled() {
		return
	}
	if _, _, err := n.pg.FanOutPendingEvents(ctx, notifyFanOutPerTick); err != nil {
		log.Printf("[notify] gagal membagikan event: %v", err)
		return
	}
	channels, err := n.pg.ListNotificationChannels(ctx)
	if err != nil {
		log.Printf("[notify] gagal membaca channel: %v", err)
		return
	}
	baseURL := n.publicBaseURL()
	for _, ch := range channels {
		if !ch.IsEnabled() {
			continue
		}
		// Satuan ukuran token bucket adalah **jumlah pesan** (setara jumlah request HTTP), bukan jumlah temuan.
		// Pada mode realtime keduanya sama (satu temuan satu pesan); pada mode ringkasan satu batch temuan digabung
		// jadi satu pesan, sehingga hanya memakai satu token.
		//
		// Kedua mode sama-sama mengecek token bucket dulu, baru mengambil sesuai kuota; urutan ini tidak boleh dibalik, kalau tidak pengiriman yang
		// terkena rate limit sudah memakai jatah retry-nya.
		now := time.Now()
		if ch.Mode == db.NotifyModeDigest {
			tokens, claimLimit := digestTickPlan()
			if n.takeTokens(ch.ID, ch.RatePerMin, tokens, now) <= 0 {
				continue
			}
			n.stepDigest(ctx, ch, claimLimit, baseURL)
			continue
		}
		allow := n.takeTokens(ch.ID, ch.RatePerMin, notifyMaxSendsPerChannelPerTick, now)
		if allow <= 0 {
			continue
		}
		n.stepRealtime(ctx, ch, allow, baseURL)
	}
}

// digestTickPlan mengembalikan konsumsi token dan batas ukuran batch untuk channel ringkasan di putaran ini.
//
// Kedua nilai balik ini punya **dimensi yang berbeda**, inilah alasan dipisah jadi fungsi sendiri:
//
//   - tokens adalah jumlah pesan. Satu batch temuan digabung jadi satu pesan, satu request HTTP, jadi nilainya selalu 1.
//     rate_per_min karena itu tetap berlaku untuk digest (maksimal sekian pesan ringkasan per menit).
//   - claimLimit adalah jumlah maksimum temuan yang ditampung satu batch ini. Ini hanya dibatasi oleh batas memori, tidak terkait anggaran request.
//
// Pernah, demi membuat rate_per_min berlaku untuk digest, anggaran request per putaran
// (notifyMaxSendsPerChannelPerTick, diturunkan dari lease) langsung dipakai sebagai ukuran batch.
// Akibatnya channel dengan rate_per_min=20 pada tick 3 detik hanya terisi 1 token, sehingga setiap pesan ringkasan
// cuma memuat 1 temuan; digest berubah jadi "notifikasi realtime berbalut teks ringkasan", pembaca menerima serangkaian pesan
// "1 temuan baru dalam 30 menit terakhir", sementara db.MaxDigestBatchSize tidak akan pernah tercapai.
//
// Gejala ini sulit terdeteksi di test end-to-end (kasus uji yang ada selalu memberi limit yang cukup besar secara manual ke
// stepDigest, sehingga melewati perhitungan kuota di step), jadi keputusan ini dikunci di sini oleh
// TestDigestTickPlanDecouplesBatchSizeFromSendBudget.
func digestTickPlan() (tokens, claimLimit int) {
	return 1, db.MaxDigestBatchSize
}

// stepRealtime mengambil dan mengirim tugas realtime suatu channel, satu temuan satu pesan.
func (n *Notifier) stepRealtime(ctx context.Context, ch *db.NotificationChannel, allow int, baseURL string) {
	deliveries, err := n.pg.ClaimRealtimeDeliveries(ctx, ch.ID, allow, notifyLease)
	if err != nil {
		log.Printf("[notify] gagal mengambil pengiriman realtime channel=%d: %v", ch.ID, err)
		return
	}
	if len(deliveries) == 0 {
		return
	}
	channel, cfg, ok := n.adapt(ch)
	if !ok {
		_ = n.pg.FailDeliveries(ctx, deliveryIDs(deliveries), fmt.Sprintf("tipe channel %q belum terdaftar", ch.Kind))
		return
	}
	for _, dl := range deliveries {
		msg, err := n.renderSingle(ctx, dl, baseURL)
		if err != nil {
			// Kegagalan render adalah masalah data lokal, retry tidak akan memperbaikinya.
			_ = n.pg.FailDeliveries(ctx, []int64{dl.ID}, err.Error())
			continue
		}
		n.send(ctx, channel, cfg, msg, []*db.NotificationDelivery{dl})
	}
}

// stepDigest menggabungkan pengiriman tertunda suatu channel menjadi satu pesan saat batch sudah waktunya.
func (n *Notifier) stepDigest(ctx context.Context, ch *db.NotificationChannel, allow int, baseURL string) {
	window := n.digestInterval()
	due, err := n.pg.DigestBatchDue(ctx, ch.ID, window)
	if err != nil {
		log.Printf("[notify] gagal menentukan batch ringkasan channel=%d: %v", ch.ID, err)
		return
	}
	if !due {
		return
	}
	deliveries, err := n.pg.ClaimDigestBatch(ctx, ch.ID, allow, notifyLease)
	if err != nil {
		log.Printf("[notify] gagal mengambil batch ringkasan channel=%d: %v", ch.ID, err)
		return
	}
	if len(deliveries) == 0 {
		return
	}
	channel, cfg, ok := n.adapt(ch)
	if !ok {
		_ = n.pg.FailDeliveries(ctx, deliveryIDs(deliveries), fmt.Sprintf("tipe channel %q belum terdaftar", ch.Kind))
		return
	}
	msg, included, err := n.renderBatch(ctx, deliveries, baseURL, int(window.Minutes()))
	if err != nil {
		_ = n.pg.FailDeliveries(ctx, deliveryIDs(deliveries), err.Error())
		return
	}
	// Pengiriman dengan snapshot rusak yang gagal masuk ke pesan harus ditandai gagal secara eksplisit. Jika tidak, mereka akan tertinggal di luar
	// included, tidak masuk pesan maupun daftar gagal; saat pengiriman sukses statusnya akan terlewat
	// oleh penandaan batch berikutnya dan selamanya terhenti di status sending sampai lease-nya habis dan diambil ulang berulang kali.
	if skipped := excludeDeliveries(deliveries, included); len(skipped) > 0 {
		reason := "snapshot event tidak dapat diparsing, temuan ini tidak dapat dirender menjadi pesan"
		if fErr := n.pg.FailDeliveries(ctx, deliveryIDs(skipped), reason); fErr != nil {
			log.Printf("[notify] gagal menandai pengiriman dengan snapshot rusak channel=%s ids=%v: %v", ch.Kind, deliveryIDs(skipped), fErr)
		}
		log.Printf("[notify] melewati %d pengiriman dengan snapshot yang tidak dapat diparsing channel=%d", len(skipped), ch.ID)
	}
	// Hanya menyerahkan yang berhasil masuk pesan ke send: included[i] dan msg.Items[i] berkorespondensi ketat,
	// send bergantung pada korespondensi ini untuk memetakan "channel melaporkan berhasil memuat K item pertama" ke baris pengiriman yang benar.
	n.send(ctx, channel, cfg, msg, included)
}

// send mengirim dan memindahkan status berdasarkan hasilnya.
//
// Satu batch pengiriman (pada mode ringkasan bisa puluhan) berbagi satu hasil kirim: berhasil terkirim atau diulang satu batch sekaligus.
// Tidak ada retry per item; pesan ringkasan hanya satu, mengirim ulang sebagian saja akan merusak semantik batch.
//
// Satu-satunya pengecualian adalah **pemotongan akibat batas panjang channel**: jika channel melaporkan hanya berhasil memuat K item pertama,
// maka item ke-(K+1) dan seterusnya harus disisakan untuk batch berikutnya, bukan ditandai berhasil bersama yang lain. Jika tidak, temuan yang terpotong
// itu tidak ada di pesan maupun di daftar gagal, dan hilang begitu saja.
func (n *Notifier) send(ctx context.Context, channel notify.Channel, cfg map[string]any, msg notify.Message, deliveries []*db.NotificationDelivery) {
	// Satu kali pengiriman diberi batas waktu, agar satu channel yang macet tidak menahan semua channel lain di putaran ini.
	sendCtx, cancel := context.WithTimeout(ctx, notifySendTimeout)
	defer cancel()
	delivered, err := channel.Send(sendCtx, cfg, msg)
	if err == nil && delivered > 0 {
		if delivered > len(deliveries) {
			// Jumlah yang dilaporkan channel seharusnya tidak melebihi jumlah pengiriman; jika ini terjadi berarti ada kesalahan hitung di layer render,
			// lebih baik dianggap semua terkirim dan masalahnya dicatat daripada membuat data jadi berantakan.
			log.Printf("[notify] channel melaporkan %d terkirim melebihi %d pengiriman channel=%s, dianggap semua terkirim",
				delivered, len(deliveries), channel.Kind())
			delivered = len(deliveries)
		}
		sent, rest := deliveries[:delivered], deliveries[delivered:]
		if err := n.pg.MarkDeliveriesSent(ctx, deliveryIDs(sent)); err != nil {
			log.Printf("[notify] gagal menandai sudah terkirim channel=%s ids=%v: %v", channel.Kind(), deliveryIDs(sent), err)
		}
		if len(rest) > 0 {
			// Pesan ini sudah mencapai batas panjang channel: sisanya langsung dikembalikan ke antrean untuk dilanjutkan di tick berikutnya.
			// Memakai DeferDeliveries, bukan RescheduleDeliveries, karena ini bukan kegagalan
			// dan tidak boleh memakai anggaran retry (saat diambil sudah ditambah +1 secara optimis, itu akan dikurangi kembali di sana).
			if err := n.pg.DeferDeliveries(ctx, deliveryIDs(rest),
				fmt.Sprintf("pesan ini sudah mencapai batas panjang channel, hanya %d item pertama yang terkirim, sisanya disisakan untuk batch berikutnya", delivered)); err != nil {
				log.Printf("[notify] gagal mengantrekan ulang sisa pengiriman bertahap channel=%s ids=%v: %v", channel.Kind(), deliveryIDs(rest), err)
			}
		}
		return
	}
	if err == nil {
		// Channel tidak melaporkan error maupun jumlah yang terkirim. Diperlakukan sebagai gagal (masuk jalur backoff),
		// agar pengiriman ini tidak diambil ulang berkali-kali tapi tidak pernah bisa ditandai.
		err = fmt.Errorf("channel tidak melaporkan jumlah yang terkirim (delivered=%d)", delivered)
	}

	// Penanganan kegagalan ditentukan **per item**, bukan berdasarkan jumlah percobaan maksimum dari seluruh batch.
	//
	// Dulu pakai `if maxAttempts(deliveries) >= MaxNotifyAttempts` untuk menggagalkan seluruh batch, tapi jumlah percobaan tiap item dalam satu batch
	// bisa berbeda: pengiriman lama yang sudah dicoba dua kali (attempts=2) akan menyeret pengiriman baru dalam batch yang sama
	// (attempts=1) ikut masuk failed; temuan baru jadi hilang permanen tanpa sempat dicoba ulang sama sekali,
	// bertentangan dengan tujuan awal "jangan sampai baris lama menyeret baris baru".
	permanent := notify.IsPermanent(err)
	var failIDs, exhaustedIDs []int64
	byDelay := map[time.Duration][]int64{}
	for _, dl := range deliveries {
		switch {
		case permanent:
			failIDs = append(failIDs, dl.ID)
		case dl.Attempts >= db.MaxNotifyAttempts:
			exhaustedIDs = append(exhaustedIDs, dl.ID)
		default:
			delay := notifyBackoff[min(dl.Attempts, len(notifyBackoff)-1)]
			byDelay[delay] = append(byDelay[delay], dl.ID)
		}
	}

	if len(failIDs) > 0 {
		if fErr := n.pg.FailDeliveries(ctx, failIDs, err.Error()); fErr != nil {
			log.Printf("[notify] gagal menandai status gagal channel=%s ids=%v: %v", channel.Kind(), failIDs, fErr)
		}
	}
	if len(exhaustedIDs) > 0 {
		reason := fmt.Sprintf("masih gagal setelah %d kali percobaan: %s", db.MaxNotifyAttempts, err)
		if fErr := n.pg.FailDeliveries(ctx, exhaustedIDs, reason); fErr != nil {
			log.Printf("[notify] gagal menandai status gagal channel=%s ids=%v: %v", channel.Kind(), exhaustedIDs, fErr)
		}
	}
	// Dijadwalkan ulang per grup berdasarkan delay: hanya ada 3 tingkat backoff, jumlah grup secara alami kecil, tidak perlu mengirim
	// UPDATE terpisah untuk setiap item (itu akan membuat batch 500 item menghasilkan 500 kali round-trip).
	for delay, group := range byDelay {
		if rErr := n.pg.RescheduleDeliveries(ctx, group, delay, err.Error()); rErr != nil {
			log.Printf("[notify] gagal menjadwalkan ulang pengiriman channel=%s ids=%v: %v", channel.Kind(), group, rErr)
		}
	}
	if len(failIDs)+len(exhaustedIDs) > 0 {
		log.Printf("[notify] pengiriman gagal channel=%d kind=%s gagal_permanen=%d retry_habis=%d menunggu_retry=%d: %s",
			deliveries[0].ChannelID, channel.Kind(), len(failIDs), len(exhaustedIDs), len(byDelay), err)
	}
}

// excludeDeliveries mengembalikan elemen dari all yang tidak ada di keep (dibandingkan berdasarkan identitas pointer).
// Dipakai untuk menemukan pengiriman yang "gagal masuk ke pesan"; pengiriman ini harus ditangani secara eksplisit, tidak boleh dibiarkan di area abu-abu.
func excludeDeliveries(all, keep []*db.NotificationDelivery) []*db.NotificationDelivery {
	inKeep := make(map[*db.NotificationDelivery]bool, len(keep))
	for _, dl := range keep {
		inKeep[dl] = true
	}
	var out []*db.NotificationDelivery
	for _, dl := range all {
		if !inKeep[dl] {
			out = append(out, dl)
		}
	}
	return out
}

// adapt mengambil implementasi channel dan mem-parsing konfigurasinya.
// Nilai balik ok=false berarti tipe belum terdaftar, pengiriman harus langsung ditandai gagal, bukan dicoba ulang tanpa batas.
func (n *Notifier) adapt(ch *db.NotificationChannel) (notify.Channel, map[string]any, bool) {
	channel, ok := notify.Get(ch.Kind)
	if !ok {
		return nil, nil, false
	}
	var cfg map[string]any
	if len(ch.Config) > 0 {
		// Saat parsing konfigurasi gagal, berikan map kosong: Validate milik channel sendiri akan melaporkan "field mana yang hilang",
		// error itu lebih membantu pengguna memperbaikinya dibanding error parsing JSON.
		_ = json.Unmarshal(ch.Config, &cfg)
	}
	if cfg == nil {
		cfg = map[string]any{}
	}
	return channel, cfg, true
}

// renderSingle merender satu pesan temuan.
func (n *Notifier) renderSingle(ctx context.Context, dl *db.NotificationDelivery, baseURL string) (notify.Message, error) {
	snap, err := parseSnapshot(dl)
	if err != nil {
		return notify.Message{}, err
	}
	item, err := n.itemFor(ctx, snap, baseURL)
	if err != nil {
		return notify.Message{}, err
	}
	return notify.Message{Items: []notify.Item{item}, HomeURL: baseURL}, nil
}

// renderBatch merender pesan ringkasan. Setiap snapshot diparsing satu per satu; jika satu item rusak, hanya item itu yang dilewati,
// tidak sampai menggagalkan seluruh batch ringkasan.
//
// Nilai balik included dan msg.Items **berkorespondensi ketat satu-satu** (pengiriman ke-i <-> item ke-i).
// Korespondensi ini adalah syarat mutlak: pemanggil menentukan K pengiriman pertama ditandai terkirim berdasarkan
// "channel melaporkan berhasil memuat K item pertama". Jika di sini snapshot yang rusak dilewati tapi pengiriman yang dilewati tidak dikeluarkan dari included,
// indeksnya akan bergeser; item buruk yang seharusnya gagal akan ditandai terkirim, sedangkan item baik malah dianggap gagal terkirim.
// Item yang rusak ditandai gagal secara eksplisit oleh pemanggil, lihat stepDigest.
func (n *Notifier) renderBatch(ctx context.Context, deliveries []*db.NotificationDelivery, baseURL string, windowMinutes int) (notify.Message, []*db.NotificationDelivery, error) {
	items := make([]notify.Item, 0, len(deliveries))
	included := make([]*db.NotificationDelivery, 0, len(deliveries))
	for _, dl := range deliveries {
		snap, err := parseSnapshot(dl)
		if err != nil {
			// Snapshot yang rusak tidak masuk pesan maupun included, penanganannya menjadi tanggung jawab pemanggil
			// (ditandai gagal secara eksplisit, bukan disamarkan sebagai "terkirim").
			log.Printf("[notify] melewati snapshot yang tidak dapat diparsing dalam batch ringkasan delivery=%d: %v", dl.ID, err)
			continue
		}
		item, err := n.itemFor(ctx, snap, baseURL)
		if err != nil {
			return notify.Message{}, nil, err
		}
		items = append(items, item)
		included = append(included, dl)
	}
	if len(items) == 0 {
		return notify.Message{}, nil, fmt.Errorf("seluruh %d pengiriman dalam batch ringkasan tidak dapat diparsing", len(deliveries))
	}
	return notify.Message{
		Items:         items,
		Batch:         true,
		WindowMinutes: windowMinutes,
		HomeURL:       baseURL,
	}, included, nil
}

// itemFor merender snapshot event menjadi item yang akan dikirim, sekaligus mem-parsing nama aset dan tautan balik detail.
func (n *Notifier) itemFor(ctx context.Context, snap notify.Snapshot, baseURL string) (notify.Item, error) {
	assets, err := n.pg.NotificationAssetNames(ctx, snap.AssetIDs)
	if err != nil {
		// Kegagalan mem-parsing nama aset tidak boleh menghalangi pengiriman: tidak bisa membaca nama jauh lebih ringan dibanding tidak menerima notifikasi sama sekali,
		// pesan hanya kehilangan satu baris aset.
		log.Printf("[notify] gagal mem-parsing nama aset finding=%d: %v", snap.FindingID, err)
	}
	item := notify.Item{
		FindingID:  snap.FindingID,
		Name:       snap.Name,
		VulnClass:  snap.VulnClass,
		Severity:   snap.Severity,
		Summary:    snap.Summary,
		Assets:     assets,
		FromStatus: snap.FromStatus,
		ToStatus:   snap.ToStatus,
	}
	if baseURL != "" {
		// Rute halaman detail ada di web/src/app/(main)/function/findings/detail/page.tsx,
		// halaman itu membaca id temuan dari parameter query id.
		item.DetailURL = fmt.Sprintf("%s/function/findings/detail?id=%d", baseURL, snap.FindingID)
	}
	return item, nil
}

// takeTokens mengambil **paling banyak want** token dari token bucket suatu channel, mengembalikan jumlah token yang benar-benar didapat.
//
// Satu token = satu pesan (satu request HTTP). Pada mode realtime, pemanggil mengirim berapa pun sesuai kebutuhan;
// pada mode ringkasan satu batch temuan hanya mengirim satu pesan, jadi dikirim 1.
//
// Kapasitas bucket adalah batas per menit channel tersebut, diisi ulang dengan rate konstan. ratePerMin<=0 berarti tidak ada rate limit,
// mengembalikan nilai terbatas tapi cukup besar, agar satu putaran loop tidak tertahan oleh backlog tanpa batas.
//
// Batas want ini diperlukan: tanpa itu bucket hanya bisa dikosongkan seluruhnya, padahal pemanggil sendiri punya batas per putaran,
// token yang diambil berlebih tidak terpakai dan hilang begitu saja sebelum pengisian berikutnya, kapasitas burst yang terkumpul jadi tidak akan pernah tercapai,
// bahkan saat "putaran ini tidak ada pengiriman tertunda sama sekali" pun akan tetap terpotong.
func (n *Notifier) takeTokens(channelID int64, ratePerMin, want int, now time.Time) int {
	if want <= 0 {
		return 0
	}
	if ratePerMin <= 0 {
		return min(want, notifyUnlimitedBurstPerTick)
	}
	n.mu.Lock()
	defer n.mu.Unlock()
	b := n.buckets[channelID]
	if b == nil {
		b = &notifyBucket{tokens: float64(ratePerMin), lastFill: now}
		n.buckets[channelID] = b
	}
	// Diisi ulang berdasarkan waktu nyata yang berlalu, rate-nya ratePerMin/60 per detik.
	if elapsed := now.Sub(b.lastFill).Seconds(); elapsed > 0 {
		b.tokens = minF(float64(ratePerMin), b.tokens+elapsed*float64(ratePerMin)/60)
		b.lastFill = now
	}
	// Tambahkan epsilon sangat kecil sebelum dibulatkan: jumlah token hasil akumulasi floating point, saat diisi ulang dua kali
	// 0.5 + 0.5 bisa jadi 0.9999999999, kalau langsung int() akan terpotong jadi 0,
	// bucket yang secara matematis sudah penuh malah tidak bisa mengambil token. 1e-9 jauh lebih kecil dari satu token, tidak akan meloloskan kekurangan yang nyata.
	take := min(int(b.tokens+1e-9), want)
	if take <= 0 {
		return 0
	}
	b.tokens -= float64(take)
	return take
}

// enabled membaca saklar utama.
func (n *Notifier) enabled() bool {
	return n.pg.GetBool(settingNotifyEnabled, true)
}

// publicBaseURL mengembalikan alamat eksternal untuk tautan balik, tanpa garis miring di akhir.
func (n *Notifier) publicBaseURL() string {
	v, ok, err := n.pg.GetSetting(settingNotifyPublicBaseURL)
	if err != nil || !ok {
		return ""
	}
	return trimTrailingSlash(v)
}

// digestInterval mengembalikan periode ringkasan, jatuh ke nilai default jika tidak valid atau belum dikonfigurasi.
func (n *Notifier) digestInterval() time.Duration {
	v, ok, err := n.pg.GetSetting(settingNotifyDigestMinutes)
	if err != nil || !ok {
		return time.Duration(notifyDefaultDigestMinutes) * time.Minute
	}
	m := 0
	if _, err := fmt.Sscanf(v, "%d", &m); err != nil || m <= 0 {
		return time.Duration(notifyDefaultDigestMinutes) * time.Minute
	}
	return time.Duration(m) * time.Minute
}

// parseSnapshot mem-parsing snapshot event milik suatu pengiriman.
func parseSnapshot(dl *db.NotificationDelivery) (notify.Snapshot, error) {
	var snap notify.Snapshot
	if len(dl.Snapshot) == 0 {
		return snap, fmt.Errorf("snapshot event pengiriman %d kosong", dl.ID)
	}
	if err := json.Unmarshal(dl.Snapshot, &snap); err != nil {
		return snap, fmt.Errorf("gagal mem-parsing snapshot event pengiriman %d: %w", dl.ID, err)
	}
	if snap.Kind == "" {
		// Jenis event mengikuti baris event yang ada di database; yang tersimpan di snapshot mungkin ditulis oleh versi lama.
		snap.Kind = dl.EventKind
	}
	return snap, nil
}

func deliveryIDs(deliveries []*db.NotificationDelivery) []int64 {
	out := make([]int64, 0, len(deliveries))
	for _, dl := range deliveries {
		out = append(out, dl.ID)
	}
	return out
}

func trimTrailingSlash(s string) string {
	for len(s) > 0 && s[len(s)-1] == '/' {
		s = s[:len(s)-1]
	}
	return s
}

func minF(a, b float64) float64 {
	if a < b {
		return a
	}
	return b
}
