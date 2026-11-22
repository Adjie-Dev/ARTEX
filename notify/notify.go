// Package notify mengimplementasikan lapisan adapter channel pengiriman IM /
// email untuk temuan kerentanan.
//
// Lapisan: paket ini adalah **leaf package**, hanya bergantung pada standard
// library. Paket ini tidak mengenal database, tidak mengenal server. Konfigurasi
// channel dikirim sebagai map[string]any (sesuai kolom JSONB
// notification_channels.config), sedangkan konten yang dikirim dibawa lewat
// Message. Keuntungan pemisahan ini: perhitungan tanda tangan, pemotongan
// UTF-8, dan pencocokan filter, hal-hal yang sebenarnya rawan bug, bisa diuji
// unit tanpa PostgreSQL; host hanya perlu melakukan orkestrasi di sisi server.
//
// Aturan konkurensi: implementasi Channel harus **stateless**. Instance
// Channel yang sama bisa dipakai bersamaan oleh beberapa konfigurasi channel
// (bahkan beberapa instance bot dari channel yang sama), jadi semua kredensial
// harus selalu dikirim lewat parameter cfg, tidak boleh menyimpan hal seperti
// webhook URL ke dalam field milik implementasi itu sendiri.
package notify

// Penanda jenis channel. Nilainya sekaligus menjadi himpunan nilai valid untuk
// notification_channels.kind, divalidasi lewat whitelist di sisi server (sama
// seperti findings.status, tidak memakai DB CHECK, agar lebih mudah menambah
// channel baru).
const (
	KindDingTalk = "dingtalk" // Bot kustom DingTalk
	KindFeishu   = "feishu"   // Bot kustom Feishu (termasuk Lark)
	KindWeCom    = "wecom"    // Bot grup WeCom (WeChat Work)
	KindWebhook  = "webhook"  // Webhook umum: method/header/template JSON kustom
	KindTelegram = "telegram" // Telegram Bot API
	KindEmail    = "email"    // Email SMTP
)

// Jenis event, sesuai dengan notification_events.kind.
const (
	EventFindingCreated       = "finding_created"
	EventFindingStatusChanged = "finding_status_changed"
)

// InitKind adalah nilai default saat kind di config kosong.
const InitKind = KindDingTalk

// severityRank memetakan level kerentanan ke angka urutan yang bisa
// dibandingkan. Level yang tidak dikenal mengembalikan 0, sehingga setiap
// setelan min_severity akan menyaring level yang tidak dikenal itu, jika ragu
// jangan dikirim, untuk mencegah notifikasi palsu membanjiri channel.
var severityRank = map[string]int{
	"low":      1,
	"medium":   2,
	"high":     3,
	"critical": 4,
}

// SeverityRank mengembalikan angka urutan level; level yang tidak dikenal
// mengembalikan 0.
func SeverityRank(severity string) int { return severityRank[severity] }

// SeverityLabel mengembalikan nama level berbahasa Indonesia dengan emoji,
// dipakai untuk judul pesan dan warna kartu notifikasi.
// Level yang tidak dikenal ditampilkan apa adanya, tidak direka-reka.
func SeverityLabel(severity string) string {
	switch severity {
	case "critical":
		return "🔴 Kritis"
	case "high":
		return "🟠 Tinggi"
	case "medium":
		return "🟡 Sedang"
	case "low":
		return "🔵 Rendah"
	default:
		return severity
	}
}

// StatusLabel menerjemahkan status penanganan ke bahasa Indonesia, dipakai
// untuk pesan perubahan status.
func StatusLabel(status string) string {
	switch status {
	case "pending":
		return "Menunggu"
	case "in_progress":
		return "Sedang Ditangani"
	case "confirmed":
		return "Terkonfirmasi"
	case "resolved":
		return "Sudah Ditangani"
	case "fixed":
		return "Sudah Diperbaiki"
	case "false_positive":
		return "Positif Palsu"
	case "ignored":
		return "Diabaikan"
	case "duplicate":
		return "Duplikat"
	case "risk_accepted":
		return "Risiko Diterima"
	default:
		return status
	}
}

// AtLeast memeriksa apakah severity sudah mencapai ambang min. min kosong
// berarti tidak ada ambang batas, selalu lolos.
// Perhatikan bahwa severity yang tidak dikenal punya angka urutan 0, sehingga
// akan ditolak oleh min yang tidak kosong (lihat komentar severityRank).
func AtLeast(severity, min string) bool {
	if min == "" {
		return true
	}
	return SeverityRank(severity) >= SeverityRank(min)
}
