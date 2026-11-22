package notify

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"strconv"
	"time"
)

// feishuChannel mengimplementasikan custom bot Feishu (termasuk Lark), menggunakan kartu interaktif.
//
// Karakteristik platform:
//   - Algoritma tanda tangan **berbeda** dari DingTalk, dan sangat mudah salah tulis, lihat komentar feishuSign.
//   - Sama seperti DingTalk, error bisnis disimpan di dalam body HTTP 200 (code != 0).
//   - Header kartu mendukung template warna, memetakan level ke warna agar tingkat keparahan terlihat sekilas di daftar pesan.
type feishuChannel struct{}

func (feishuChannel) Kind() string { return KindFeishu }

// Custom bot Feishu sekitar 5 kali/detik, setara 100 kali/menit.
func (feishuChannel) DefaultRatePerMin() int { return 100 }

// Bagian akhir alamat Webhook adalah identitas unik bot, tergolong kredensial.
func (feishuChannel) SecretKeys() []string { return []string{"webhook", "secret"} }

// Sama halnya: mengubah alamat Webhook berarti kunci tanda tangan harus dinyatakan ulang untuk alamat baru.
func (feishuChannel) DestinationKeys() []string { return []string{"webhook"} }

func (feishuChannel) Validate(cfg map[string]any) error {
	hook := cfgString(cfg, "webhook")
	if hook == "" {
		return errors.New("alamat Webhook tidak ada")
	}
	if err := validateHTTPURL(hook); err != nil {
		return fmt.Errorf("alamat Webhook tidak valid: %w", err)
	}
	return nil
}

func (c feishuChannel) Send(ctx context.Context, cfg map[string]any, m Message) (int, error) {
	if err := c.Validate(cfg); err != nil {
		return 0, Permanent(err)
	}
	card, kept := feishuCard(m)
	payload := map[string]any{
		"msg_type": "interactive",
		"card":     card,
	}
	// Parameter tanda tangan berada di level yang sama dengan pesan, dan hanya muncul jika secret dikonfigurasi.
	if secret := cfgString(cfg, "secret"); secret != "" {
		ts := strconv.FormatInt(time.Now().Unix(), 10)
		payload["timestamp"] = ts
		payload["sign"] = feishuSign(ts, secret)
	}
	raw, err := doJSON(ctx, "POST", cfgString(cfg, "webhook"), nil, payload)
	if err != nil {
		return 0, err
	}
	var res struct {
		Code int    `json:"code"`
		Msg  string `json:"msg"`
		// Beberapa versi hook Feishu memakai nama field ini, ikut didukung di sini.
		StatusCode    int    `json:"StatusCode"`
		StatusMessage string `json:"StatusMessage"`
	}
	if err := json.Unmarshal(raw, &res); err != nil {
		return 0, fmt.Errorf("gagal mengurai respons Feishu: %w (%s)", err, snippet(raw))
	}
	if res.Code != 0 {
		return 0, Permanent(fmt.Errorf("Feishu mengembalikan error %d: %s", res.Code, res.Msg))
	}
	if res.StatusCode != 0 {
		return 0, Permanent(fmt.Errorf("Feishu mengembalikan error %d: %s", res.StatusCode, res.StatusMessage))
	}
	return kept, nil
}

// feishuSign menghitung tanda tangan sesuai aturan resmi Feishu.
//
// Bagian ini sangat mudah salah: contoh resmi adalah
//
//	hmac.new(string_to_sign.encode(), digestmod=sha256)
//
// yang berarti **key = timestamp + "\n" + secret, message kosong**, bukan yang terasa intuitif yaitu
// "key=secret, message=stringToSign", yang justru itulah algoritma DingTalk. Kedua algoritma ini terbalik satu
// sama lain, sehingga menulis sesuai implementasi platform yang lain pasti membuat verifikasi tanda tangan gagal (error 19021).
func feishuSign(timestamp, secret string) string {
	stringToSign := timestamp + "\n" + secret
	mac := hmac.New(sha256.New, []byte(stringToSign))
	return base64.StdEncoding.EncodeToString(mac.Sum(nil))
}

// feishuSeverityTemplate memetakan level temuan ke template warna header kartu.
// Level yang tidak dikenal memakai grey, bukan blue, agar tidak tertukar dengan low.
func feishuSeverityTemplate(severity string) string {
	switch severity {
	case "critical":
		return "red"
	case "high":
		return "orange"
	case "medium":
		return "yellow"
	case "low":
		return "blue"
	default:
		return "grey"
	}
}

// feishuMaxCardBytes adalah batas konservatif untuk ukuran isi kartu. Feishu membatasi ukuran kartu, dan jika
// melebihi, seluruh pesan ditolak; nilai ini diambil jauh di bawah batas resmi, sudah memperhitungkan overhead pembungkus JSON.
const feishuMaxCardBytes = 24000

// feishuCard membangun kartu interaktif, mengembalikan kartu beserta **jumlah item yang benar-benar ditulis**.
// Fungsi kept sama seperti pada markdownBody: hanya item yang benar-benar masuk ke kartu yang boleh ditandai sudah terkirim.
func feishuCard(m Message) (map[string]any, int) {
	elements := []any{}
	kept := 0
	if m.Batch {
		// Paket per item dulu, baru header disusun: header perlu menulis "N item lainnya akan dilanjutkan pada
		// pesan berikutnya", dan N harus berasal dari jumlah item yang benar-benar tertampung.
		kept = packItemCount(m.Items, feishuMaxCardBytes, markdownReservedBytes, "", byteSize, func(it Item, idx int) string {
			return feishuBatchLine(it, idx+1)
		})
		items := m.Items[:kept]
		elements = append(elements, feishuMarkdownDiv(markdownBatchIntro(m, items, len(m.Items))))
		for i, it := range items {
			elements = append(elements, feishuMarkdownDiv(feishuBatchLine(it, i+1)))
		}
		if m.HomeURL != "" {
			elements = append(elements, feishuButton("Lihat semua di platform", m.HomeURL))
		}
	} else if len(m.Items) > 0 {
		kept = 1
		it := m.Items[0]
		elements = append(elements, feishuMarkdownDiv(feishuItemLines(it)))
		if it.DetailURL != "" {
			elements = append(elements, feishuButton("Lihat Detail", it.DetailURL))
		}
	}

	card := map[string]any{
		"config":   map[string]any{"wide_screen_mode": true},
		"header":   map[string]any{"title": map[string]any{"tag": "plain_text", "content": markdownTitle(m)}},
		"elements": elements,
	}
	if len(m.Items) > 0 {
		card["header"].(map[string]any)["template"] = feishuSeverityTemplate(m.Items[0].Severity)
	}
	return card, kept
}

func feishuMarkdownDiv(content string) map[string]any {
	return map[string]any{"tag": "div", "text": map[string]any{"tag": "lark_md", "content": content}}
}

func feishuButton(label, url string) map[string]any {
	return map[string]any{
		"tag": "action",
		"actions": []any{map[string]any{
			"tag":  "button",
			"text": map[string]any{"tag": "lark_md", "content": label},
			"url":  url,
			"type": "primary",
		}},
	}
}

// feishuItemLines merender body lark_md untuk satu temuan.
//
// lark_md adalah format teks serumpun dengan markdown, yang juga mem-parsing link dan penekanan, sehingga semua
// field yang berasal dari luar selalu melewati markdownText (dijadikan satu baris + escaping). Tanpa ini, satu
// judul temuan saja bisa berubah menjadi tautan eksternal yang bisa diklik di Feishu.
func feishuItemLines(it Item) string {
	out := fmt.Sprintf("**%s · %s**", SeverityLabel(it.Severity), markdownText(it.Title(), 0))
	if it.IsStatusChange() {
		out += fmt.Sprintf("\n**Perubahan status**: %s -> %s",
			markdownText(StatusLabel(it.FromStatus), 0), markdownText(StatusLabel(it.ToStatus), 0))
	}
	if it.VulnClass != "" && it.VulnClass != it.Title() {
		out += fmt.Sprintf("\n**Tipe**: %s", markdownText(it.VulnClass, 0))
	}
	if a := assetLine(it.Assets, maxAssetsShown); a != "" {
		out += fmt.Sprintf("\n**Aset**: %s", markdownText(a, 0))
	}
	if it.Summary != "" {
		if s := markdownText(it.Summary, maxSummaryRunes); s != "" {
			out += fmt.Sprintf("\n**Ringkasan**: %s", s)
		}
	}
	return out
}

// feishuBatchLine merender satu baris di dalam kartu ringkasan.
func feishuBatchLine(it Item, index int) string {
	line := fmt.Sprintf("**%d. %s · %s**", index, SeverityLabel(it.Severity), markdownText(it.Title(), 0))
	if a := assetLine(it.Assets, maxAssetsShown); a != "" {
		line += " — " + markdownText(a, 0)
	}
	return line
}
