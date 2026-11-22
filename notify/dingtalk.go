package notify

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"net/url"
	"strconv"
	"time"
)

// dingTalkChannel mengimplementasikan custom bot DingTalk.
//
// Karakteristik platform (yang menentukan pilihan implementasi di sini):
//   - Satu bot dibatasi 20 pesan/menit, kelebihan kirim akan dibuang secara diam-diam (HTTP tetap bisa 200),
//     sehingga pembatasan laju harus dilakukan di sisi klien, lihat DefaultRatePerMin.
//   - Pengaturan keamanan dipilih salah satu dari tiga: tanda tangan / kata kunci kustom / whitelist IP. Tanda
//     tangan adalah satu-satunya cara yang tidak bergantung pada isi pesan, sehingga hanya tanda tangan yang
//     didukung (juga mendukung webhook polos jika ketiganya tidak diaktifkan).
//   - Berhasil atau gagal keduanya mengembalikan HTTP 200, dibedakan lewat errcode di body; tidak memeriksa
//     errcode akan membuat kegagalan pengiriman tercatat sebagai berhasil.
type dingTalkChannel struct{}

func (dingTalkChannel) Kind() string { return KindDingTalk }

func (dingTalkChannel) DefaultRatePerMin() int { return 20 }

// Alamat Webhook DingTalk membawa access_token, yang sendirinya merupakan kredensial, sehingga disamarkan seluruhnya.
func (dingTalkChannel) SecretKeys() []string { return []string{"webhook", "secret"} }

// Tujuannya adalah alamat Webhook DingTalk itu sendiri; mengubah alamat berarti kunci tanda tangan juga harus dinyatakan ulang untuk alamat baru.
func (dingTalkChannel) DestinationKeys() []string { return []string{"webhook"} }

func (dingTalkChannel) Validate(cfg map[string]any) error {
	hook := cfgString(cfg, "webhook")
	if hook == "" {
		return errors.New("alamat Webhook tidak ada")
	}
	if err := validateHTTPURL(hook); err != nil {
		return fmt.Errorf("alamat Webhook tidak valid: %w", err)
	}
	return nil
}

// Send mengirim satu pesan. Jika ada link balik dan hanya satu item, memakai ActionCard (dengan tombol), selain itu memakai markdown.
func (c dingTalkChannel) Send(ctx context.Context, cfg map[string]any, m Message) (int, error) {
	hook := cfgString(cfg, "webhook")
	if err := c.Validate(cfg); err != nil {
		return 0, Permanent(err)
	}
	endpoint, err := dingTalkSignedURL(hook, cfgString(cfg, "secret"), time.Now())
	if err != nil {
		return 0, Permanent(err)
	}

	title := markdownTitle(m)
	// Body markdown DingTalk tidak memiliki batas byte yang pasti, tapi tetap diberi pembatas untuk mencegah field evidence membengkak secara tidak normal.
	text, kept := markdownBody(m, 20000)

	var payload any
	if !m.Batch && len(m.Items) == 1 && m.Items[0].DetailURL != "" {
		payload = map[string]any{
			"msgtype": "actionCard",
			"actionCard": map[string]any{
				"title":          title,
				"text":           text,
				"btnOrientation": "0",
				"singleTitle":    "Lihat Detail",
				"singleURL":      m.Items[0].DetailURL,
			},
		}
	} else {
		payload = map[string]any{
			"msgtype":  "markdown",
			"markdown": map[string]any{"title": title, "text": text},
		}
	}

	raw, err := doJSON(ctx, "POST", endpoint, nil, payload)
	if err != nil {
		return 0, err
	}
	// DingTalk menyembunyikan error bisnis di dalam respons 200.
	var res struct {
		ErrCode int    `json:"errcode"`
		ErrMsg  string `json:"errmsg"`
	}
	if err := json.Unmarshal(raw, &res); err != nil {
		return 0, fmt.Errorf("gagal mengurai respons DingTalk: %w (%s)", err, snippet(raw))
	}
	if res.ErrCode != 0 {
		// 301000 berarti verifikasi tanda tangan gagal, 310000 berarti kata kunci tidak cocok, keduanya adalah
		// kesalahan konfigurasi, tidak akan sembuh sendiri lewat percobaan ulang.
		return 0, Permanent(fmt.Errorf("DingTalk mengembalikan error %d: %s", res.ErrCode, res.ErrMsg))
	}
	return kept, nil
}

// dingTalkSignedURL menambahkan parameter timestamp dan sign ke webhook sesuai aturan tanda tangan resmi.
//
// Aturannya: string yang ditandatangani = timestamp + "\n" + secret, dengan **kunci HMAC-SHA256 juga secret**,
// hasilnya di-encode base64 lalu di-encode URL. timestamp dalam milidetik. Jika secret kosong, dikembalikan apa
// adanya, untuk mendukung bot yang tidak mengaktifkan tanda tangan.
func dingTalkSignedURL(hook, secret string, now time.Time) (string, error) {
	if secret == "" {
		return hook, nil
	}
	ts := strconv.FormatInt(now.UnixMilli(), 10)
	mac := hmac.New(sha256.New, []byte(secret))
	mac.Write([]byte(ts + "\n" + secret))
	sign := base64.StdEncoding.EncodeToString(mac.Sum(nil))

	u, err := url.Parse(hook)
	if err != nil {
		// err tidak diteruskan apa adanya: teks error dari url.Parse membawa alamat lengkap (termasuk access_token).
		return "", fmt.Errorf("gagal mengurai alamat Webhook: %s", redactRequestTarget(hook))
	}
	q := u.Query()
	q.Set("timestamp", ts)
	q.Set("sign", sign)
	u.RawQuery = q.Encode()
	return u.String(), nil
}

// validateHTTPURL memvalidasi bahwa alamat bisa dipakai, protokolnya didukung, dan memeriksa jaringan internal
// untuk target berupa IP literal.
//
// Dua hal yang diperhatikan dengan saksama:
//
//  1. **Pesan error harus disamarkan**. url.Parse sendiri mengembalikan *url.Error, dan Error()-nya membawa
//     **alamat asli lengkap**, sementara alamat pada beberapa platform ini menyimpan kredensial di dalamnya
//     (access_token DingTalk, key WeCom, bot token Telegram, hook id Feishu). Dulu di sini langsung
//     `return err`, sehingga error "format alamat tidak valid" membawa keluar kredensial, mengalir ke respons 400
//     endpoint uji, kolom last_error setiap pengiriman yang tersimpan, log server, dan endpoint riwayat pengiriman.
//
//  2. **IP literal langsung diperiksa sebagai jaringan internal**, sementara nama domain diserahkan ke tahap dial
//     (blockInternalDial adalah titik yang benar-benar menentukan, dan juga bisa menutup DNS rebinding). Pemeriksaan
//     di sini dilakukan sekali lagi agar peringatan sudah muncul saat konfigurasi disimpan, bukan menunggu sampai
//     pengiriman pertama gagal.
//
// Pembatasan protokol bersifat defensif: skema seperti file:///gopher:// bisa membuat http.Client berperilaku
// di luar ekspektasi (meski sudah dicegah oleh pemeriksaan scheme, tidak ada alasan untuk membuka celah ini).
func validateHTTPURL(raw string) error {
	u, err := url.Parse(raw)
	if err != nil {
		return fmt.Errorf("alamat tidak dapat diurai (%s)", redactRequestTarget(raw))
	}
	if u.Scheme != "http" && u.Scheme != "https" {
		return fmt.Errorf("hanya mendukung http/https, menerima %q", u.Scheme)
	}
	if u.Host == "" {
		return errors.New("nama host tidak ada")
	}
	if ip := net.ParseIP(u.Hostname()); ip != nil && isBlockedDialIP(ip) && !allowLocalTargets() {
		return fmt.Errorf("pengiriman ke alamat lokal/link-local %s ditolak (jika memang perlu mengirim ke layanan lokal, set %s=1)", ip, AllowLocalTargetsEnv)
	}
	return nil
}
