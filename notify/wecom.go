package notify

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
)

// weComMarkdownLimit adalah batas keras (hard limit) untuk markdown content
// bot grup WeCom (byte, bukan karakter).
// Ini adalah batasan paling ketat di antara keenam channel, dan menjadi alasan
// utama keberadaan TruncateBytes.
const weComMarkdownLimit = 4096

// weComChannel mengimplementasikan bot grup WeCom (WeChat Work).
//
// Karakteristik platform:
//   - Satu-satunya autentikasi lewat key di URL, tidak mendukung signing,
//     sehingga alamat webhook itu sendiri adalah seluruh kredensialnya.
//   - Batas markdown content 4096 **byte**, jika melebihi seluruh pesan
//     ditolak (bukan dipotong). Karakter non-ASCII bisa memakai 3 byte/huruf,
//     artinya isi pesan hanya bisa menampung sekitar seribu karakter lebih,
//     jadi pemotongan wajib dilakukan di sisi klien.
//   - Rate limit 20 pesan/menit, juga ditangani lewat pembatasan di sisi klien.
type weComChannel struct{}

func (weComChannel) Kind() string { return KindWeCom }

func (weComChannel) DefaultRatePerMin() int { return 20 }

// WeCom hanya punya satu kredensial, yaitu Webhook (key di URL), dan tidak
// mendukung signing, jadi seluruh alamat itu adalah kredensialnya, tidak ada
// field lain yang perlu disamarkan.
func (weComChannel) SecretKeys() []string { return []string{"webhook"} }

// WeCom hanya punya satu field, yaitu Webhook, yang sekaligus menjadi tujuan
// dan kredensial, sehingga tidak ada "kredensial sisa setelah alamat diubah".
func (weComChannel) DestinationKeys() []string { return []string{"webhook"} }

func (weComChannel) Validate(cfg map[string]any) error {
	hook := cfgString(cfg, "webhook")
	if hook == "" {
		return errors.New("alamat Webhook tidak ada")
	}
	if err := validateHTTPURL(hook); err != nil {
		return fmt.Errorf("alamat Webhook tidak valid: %w", err)
	}
	return nil
}

func (c weComChannel) Send(ctx context.Context, cfg map[string]any, m Message) (int, error) {
	if err := c.Validate(cfg); err != nil {
		return 0, Permanent(err)
	}
	// Batch ringkasan bisa sangat panjang (50 item x satu baris per item plus
	// prefix), mudah melebihi 4096 byte.
	// Pemotongan dilakukan di sini, bukan mengandalkan error dari platform:
	// ditolak berarti seluruh batch hilang, sedangkan pemotongan setidaknya
	// mengirim beberapa item di awal.
	content, kept := markdownBody(m, weComMarkdownLimit)
	payload := map[string]any{
		"msgtype":  "markdown",
		"markdown": map[string]any{"content": content},
	}
	raw, err := doJSON(ctx, "POST", cfgString(cfg, "webhook"), nil, payload)
	if err != nil {
		return 0, err
	}
	var res struct {
		ErrCode int    `json:"errcode"`
		ErrMsg  string `json:"errmsg"`
	}
	if err := json.Unmarshal(raw, &res); err != nil {
		return 0, fmt.Errorf("gagal mem-parsing respons WeCom: %w (%s)", err, snippet(raw))
	}
	if res.ErrCode != 0 {
		// 45009 berarti jumlah pemanggilan API melebihi batas, jendela rate
		// limit platform akan bergeser seiring waktu, sehingga retry setelah
		// backoff memang efektif, jadi ini ditandai sebagai bisa di-retry.
		// Kalau sampai ke sini berarti rate_per_min di sisi klien diset terlalu
		// agresif, retry hanya solusi sementara, perbaikan sebenarnya adalah
		// menurunkan nilai rate limit channel ini.
		if res.ErrCode == 45009 {
			return 0, fmt.Errorf("WeCom membatasi laju permintaan %d: %s", res.ErrCode, res.ErrMsg)
		}
		// 93000 berarti webhook key tidak valid, kegagalan permanen, retry
		// tidak akan memperbaikinya sendiri.
		return 0, Permanent(fmt.Errorf("WeCom mengembalikan error %d: %s", res.ErrCode, res.ErrMsg))
	}
	return kept, nil
}
