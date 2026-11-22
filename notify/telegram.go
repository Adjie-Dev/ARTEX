package notify

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"strings"
)

// telegramTextLimit adalah batas atas field text pada Telegram sendMessage (jumlah karakter).
const telegramTextLimit = 4096

// telegramChannel mengimplementasikan Telegram Bot API.
//
// Karakteristik platform:
//   - Autentikasi seluruhnya ada di URL path (/bot<token>/sendMessage), tidak perlu tanda tangan.
//   - Memakai mode parsing HTML, bukan MarkdownV2: MarkdownV2 mewajibkan escape untuk
//     karakter `_*[]()~`>#+-=|{}.!` (total 18 karakter), lupa satu saja seluruh pesan ditolak;
//     HTML hanya perlu escape tiga karakter & < >.
//   - Error bisnis juga disembunyikan di balik HTTP 200, dideteksi lewat field ok.
type telegramChannel struct{}

func (telegramChannel) Kind() string { return KindTelegram }

// Telegram membatasi sekitar 1 pesan/detik untuk chat pribadi dan 20 pesan/menit untuk grup.
// Dipakai nilai yang konservatif.
func (telegramChannel) DefaultRatePerMin() int { return 20 }

// Bot Token adalah kredensial penuh; chat_id hanya penerima, bukan rahasia
// (punya chat_id tanpa Token tetap tidak bisa mengirim pesan).
func (telegramChannel) SecretKeys() []string { return []string{"bot_token"} }

// base_url menentukan endpoint API tujuan Token dikirim (misalnya reverse proxy sendiri);
// mengubahnya mengharuskan Token diisi ulang.
func (telegramChannel) DestinationKeys() []string { return []string{"base_url"} }

func (telegramChannel) Validate(cfg map[string]any) error {
	if cfgString(cfg, "bot_token") == "" {
		return errors.New("Bot Token belum diisi")
	}
	if cfgString(cfg, "chat_id") == "" {
		return errors.New("Chat ID belum diisi")
	}
	if base := cfgString(cfg, "base_url"); base != "" {
		if err := validateHTTPURL(base); err != nil {
			return fmt.Errorf("alamat API tidak valid: %w", err)
		}
	}
	return nil
}

func (c telegramChannel) Send(ctx context.Context, cfg map[string]any, m Message) (int, error) {
	if err := c.Validate(cfg); err != nil {
		return 0, Permanent(err)
	}
	endpoint, err := telegramEndpoint(cfg)
	if err != nil {
		return 0, Permanent(err)
	}
	text, kept := telegramHTML(m)
	payload := map[string]any{
		"chat_id":                  cfgString(cfg, "chat_id"),
		"text":                     text,
		"parse_mode":               "HTML",
		"disable_web_page_preview": false,
	}
	raw, err := doJSON(ctx, "POST", endpoint, nil, payload)
	if err != nil {
		return 0, err
	}
	var res struct {
		OK          bool   `json:"ok"`
		ErrorCode   int    `json:"error_code"`
		Description string `json:"description"`
	}
	if err := json.Unmarshal(raw, &res); err != nil {
		return 0, fmt.Errorf("gagal parsing respons Telegram: %w (%s)", err, snippet(raw))
	}
	if res.OK {
		return kept, nil
	}
	// 429 berarti dibatasi (rate limit), masih bisa berhasil setelah backoff dan dicoba ulang;
	// selebihnya (400 parameter salah, 401 token salah, 403 diblokir, 404 chat tidak ada)
	// adalah masalah konfigurasi, dicoba ulang pun tidak akan sembuh sendiri.
	if res.ErrorCode == 429 {
		return 0, fmt.Errorf("Telegram membatasi laju (rate limit): %s", res.Description)
	}
	return 0, Permanent(fmt.Errorf("Telegram mengembalikan error %d: %s", res.ErrorCode, res.Description))
}

// telegramEndpoint merangkai alamat sendMessage. Jika base_url dikosongkan, dipakai API resmi;
// jika terisi, dipakai untuk reverse proxy Bot API sendiri (kebutuhan umum di jaringan tertentu).
func telegramEndpoint(cfg map[string]any) (string, error) {
	base := cfgString(cfg, "base_url")
	if base == "" {
		base = "https://api.telegram.org"
	}
	base = strings.TrimSuffix(base, "/")
	token := cfgString(cfg, "bot_token")
	raw := base + "/bot" + token + "/sendMessage"
	u, err := url.Parse(raw)
	if err != nil {
		// Tidak meneruskan err langsung: alamatnya mengandung Bot Token, dan addr
		// sendiri pun tidak boleh ditampilkan balik pada tahap ini.
		return "", fmt.Errorf("gagal merangkai alamat API (alamat API: %s)", redactRequestTarget(base))
	}
	return u.String(), nil
}

// telegramHTML merender isi HTML, mengembalikan isi pesan dan jumlah item yang benar-benar
// ditulis (lihat Channel.Send).
func telegramHTML(m Message) (string, int) {
	var b strings.Builder
	b.WriteString("<b>" + telegramEscape(markdownTitle(m)) + "</b>\n")
	if m.Batch {
		// Batas Telegram dihitung **per karakter**, jadi pengepakan batch juga diukur per karakter (runeSize).
		footer := ""
		if m.HomeURL != "" {
			footer = fmt.Sprintf("\n\n<a href=\"%s\">Lihat semua di platform</a>", telegramEscapeAttr(m.HomeURL))
		}
		kept := packItemCount(m.Items, telegramTextLimit, telegramReservedRunes, footer, runeSize, func(it Item, idx int) string {
			return telegramBatchLine(it, idx+1)
		})
		items := m.Items[:kept]
		b.Reset()
		b.WriteString("<b>" + telegramEscape(telegramBatchTitle(m, items, len(m.Items))) + "</b>")
		for i, it := range items {
			b.WriteString("\n" + telegramEscape(telegramBatchLine(it, i+1)))
		}
		b.WriteString(footer)
		return TruncateHTML(b.String(), telegramTextLimit), kept
	}
	if len(m.Items) == 0 {
		return b.String(), 0
	}
	it := m.Items[0]
	if it.IsStatusChange() {
		b.WriteString(fmt.Sprintf("\n<b>Perubahan status</b>: %s → %s",
			telegramEscape(StatusLabel(it.FromStatus)), telegramEscape(StatusLabel(it.ToStatus))))
	}
	if it.VulnClass != "" && it.VulnClass != it.Title() {
		b.WriteString("\n<b>Jenis</b>: " + telegramEscape(it.VulnClass))
	}
	if a := assetLine(it.Assets, maxAssetsShown); a != "" {
		b.WriteString("\n<b>Aset</b>: " + telegramEscape(a))
	}
	if s := OneLine(it.Summary, maxSummaryRunes); s != "" {
		b.WriteString("\n<b>Ringkasan</b>: " + telegramEscape(s))
	}
	if it.DetailURL != "" {
		b.WriteString(fmt.Sprintf("\n\n<a href=\"%s\">Lihat detail</a>", telegramEscapeAttr(it.DetailURL)))
	}
	return TruncateHTML(b.String(), telegramTextLimit), 1
}

// telegramReservedRunes dicadangkan untuk judul pesan dan kemungkinan catatan pemotongan (dihitung per karakter).
const telegramReservedRunes = 160

// telegramBatchLine merender satu baris dalam ringkasan (belum di-escape, escape dilakukan terpusat oleh pemanggil).
func telegramBatchLine(it Item, idx int) string {
	if a := assetLine(it.Assets, maxAssetsShown); a != "" {
		return fmt.Sprintf("%d. %s · %s — %s", idx, SeverityLabel(it.Severity), it.Title(), a)
	}
	return fmt.Sprintf("%d. %s · %s", idx, SeverityLabel(it.Severity), it.Title())
}

// telegramBatchTitle merender baris judul pesan ringkasan. Jumlah yang ditampilkan adalah
// jumlah item **yang benar-benar termuat** dalam pesan ini, bukan jumlah total batch,
// agar pembaca tidak salah paham bahwa angka di judul sudah mewakili semuanya.
func telegramBatchTitle(m Message, items []Item, total int) string {
	title := fmt.Sprintf("Ringkasan temuan · total %d", total)
	if extra := total - len(items); extra > 0 {
		title += fmt.Sprintf(" (menampilkan %d pertama, %d sisanya dilanjutkan di pesan berikutnya)", len(items), extra)
	}
	if m.WindowMinutes > 0 {
		title = fmt.Sprintf("%d menit terakhir · %s", m.WindowMinutes, title)
	}
	return title
}

// telegramEscape melakukan escape pada isi teks HTML.
// Telegram hanya mengenal tiga entity ini; setelah escape, entity yang sudah ada seperti
// &amp; akan di-escape dua kali, dan itu memang perilaku yang diharapkan: yang ingin
// ditampilkan adalah karakter mentahnya, bukan membiarkan pengguna menyuntik HTML.
func telegramEscape(s string) string {
	s = strings.ReplaceAll(s, "&", "&amp;")
	s = strings.ReplaceAll(s, "<", "&lt;")
	s = strings.ReplaceAll(s, ">", "&gt;")
	return s
}

// telegramEscapeAttr melakukan escape pada nilai atribut HTML. Selain escape teks biasa,
// tanda kutip juga harus ditangani karena URL yang mengandung tanda kutip bisa menutup
// atribut href lebih awal dan mengubah sisanya menjadi titik injeksi.
func telegramEscapeAttr(s string) string {
	s = telegramEscape(s)
	s = strings.ReplaceAll(s, "\"", "&quot;")
	return s
}
