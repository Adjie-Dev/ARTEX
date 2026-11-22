package notify

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"text/template"
	"time"
)

// webhookChannel adalah adaptor Webhook generik: URL, metode, header permintaan, dan
// template JSON semuanya ditentukan pengguna sendiri. Keberadaannya membuat fitur ini
// tidak perlu menulis implementasi khusus untuk Slack / Mattermost / Discord / sistem
// buatan sendiri satu per satu, karena semua platform itu bisa dicakup oleh satu
// template yang bisa dikonfigurasi.
type webhookChannel struct{}

func (webhookChannel) Kind() string { return KindWebhook }

// Webhook generik tidak punya batas resmi, mengembalikan 0 berarti default tidak
// dibatasi laju (rate limit), diatur sendiri oleh pengguna sesuai kemampuan sisi penerima.
func (webhookChannel) DefaultRatePerMin() int { return 0 }

// Masking untuk url dan headers: alamat tujuan sering membawa token, dan header
// kustom biasanya menyimpan kredensial autentikasi, keduanya bisa muncul di
// response API sehingga harus disamarkan.
// Konsekuensinya, saat mengedit, jika ingin mengubah salah satu header, seluruh
// kumpulan header harus diisi ulang (nilai yang dimasking akan diartikan sebagai
// "tetap gunakan nilai lama"). Ini adalah trade-off yang disengaja: lebih baik
// mengisi ulang sekali lagi daripada menampilkan kembali kredensial ke browser.
func (webhookChannel) SecretKeys() []string { return []string{"url", "headers"} }

// Tujuannya adalah url. Saat url diubah, headers harus diisi ulang secara eksplisit,
// kalau tidak, header Authorization yang asli akan terkirim apa adanya ke alamat baru,
// dan ini justru menjadi jalur utama untuk membypass masking.
func (webhookChannel) DestinationKeys() []string { return []string{"url"} }

// webhookDefaultTemplate adalah isi permintaan bawaan saat template belum diisi:
// sebuah struktur JSON sederhana yang mencakup sebagian besar kebutuhan penerima
// buatan sendiri yang hanya ingin "menerima satu JSON lalu menyimpannya".
const webhookDefaultTemplate = `{
  "title": {{json .Title}},
  "batch": {{.Batch}},
  "count": {{.Count}},
  "items": [
{{- range $i, $it := .Items}}
{{- if $i}},{{end}}
    {
      "finding_id": {{$it.FindingID}},
      "name": {{json $it.Name}},
      "vulnclass": {{json $it.VulnClass}},
      "severity": {{json $it.Severity}},
      "summary": {{json $it.Summary}},
      "assets": {{json $it.Assets}},
      "detail_url": {{json $it.DetailURL}}
    }
{{- end}}
  ]
}`

// webhookTemplateData adalah context yang diekspos ke template pengguna.
type webhookTemplateData struct {
	Title   string
	Batch   bool
	Count   int
	Items   []webhookItem
	HomeURL string
	// SentAt adalah waktu pengiriman kali ini (RFC3339), untuk dicatat sisi penerima.
	SentAt string
}

type webhookItem struct {
	FindingID     int64
	Name          string
	VulnClass     string
	Severity      string
	SeverityLabel string
	Summary       string
	Assets        []string
	DetailURL     string
	FromStatus    string
	ToStatus      string
	// StatusLabel adalah deskripsi perubahan status yang mudah dibaca, seperti
	// "menunggu → sudah diperbaiki"; kosong jika bukan perubahan status.
	StatusLabel string
}

func (webhookChannel) Validate(cfg map[string]any) error {
	raw := cfgString(cfg, "url")
	if raw == "" {
		return errors.New("URL tujuan belum diisi")
	}
	if err := validateHTTPURL(raw); err != nil {
		return fmt.Errorf("URL tujuan tidak valid: %w", err)
	}
	if m := strings.ToUpper(cfgString(cfg, "method")); m != "" && m != http.MethodGet && m != http.MethodPost && m != http.MethodPut && m != http.MethodPatch {
		return fmt.Errorf("metode %s tidak didukung (gunakan GET/POST/PUT/PATCH)", m)
	}
	if tpl := cfgString(cfg, "body_template"); tpl != "" {
		if _, err := parseWebhookTemplate(tpl); err != nil {
			return fmt.Errorf("sintaks template isi permintaan salah: %w", err)
		}
	}
	return nil
}

func (c webhookChannel) Send(ctx context.Context, cfg map[string]any, m Message) (int, error) {
	if err := c.Validate(cfg); err != nil {
		return 0, Permanent(err)
	}
	method := strings.ToUpper(cfgString(cfg, "method"))
	if method == "" {
		method = http.MethodPost
	}

	// GET tidak membawa body permintaan: memasukkan isinya ke query melampaui
	// kemampuan template dan juga tidak sesuai semantik GET, sehingga GET hanya
	// cocok untuk penerima bertipe "cukup dipicu untuk menjalankan hook".
	var payload any
	if method != http.MethodGet {
		body, err := renderWebhookBody(cfgString(cfg, "body_template"), m)
		if err != nil {
			return 0, Permanent(err)
		}
		// Hasil render template berupa JSON dalam bentuk string; di sini dikonversi
		// ke json.RawMessage agar dikirim apa adanya, supaya tidak terjadi escape
		// ganda yang membungkus struktur hasil rancangan pengguna ke dalam satu
		// string JSON.
		if !json.Valid([]byte(body)) {
			return 0, Permanent(errors.New("hasil render template isi permintaan bukan JSON yang valid"))
		}
		payload = json.RawMessage(body)
	}

	headers := cfgMap(cfg, "headers")
	if ct := cfgString(cfg, "content_type"); ct != "" {
		// Diizinkan untuk menimpa, tapi diterapkan setelah headers agar
		// konfigurasi eksplisit tetap diprioritaskan.
		if headers == nil {
			headers = map[string]string{}
		}
		headers["Content-Type"] = ct
	}
	if _, err := doJSON(ctx, method, cfgString(cfg, "url"), headers, payload); err != nil {
		return 0, err
	}
	// Webhook generik tidak memotong isi body (penerima adalah layanan milik
	// pengguna sendiri, ukurannya ditentukan oleh body_template), sehingga
	// seluruh batch dianggap terkirim.
	return len(m.Items), nil
}

// renderWebhookBody merender isi permintaan menggunakan template pengguna (atau template bawaan).
func renderWebhookBody(tpl string, m Message) (string, error) {
	if strings.TrimSpace(tpl) == "" {
		tpl = webhookDefaultTemplate
	}
	t, err := parseWebhookTemplate(tpl)
	if err != nil {
		return "", fmt.Errorf("sintaks template isi permintaan salah: %w", err)
	}
	var buf bytes.Buffer
	if err := t.Execute(&buf, newWebhookTemplateData(m)); err != nil {
		return "", fmt.Errorf("gagal merender template isi permintaan: %w", err)
	}
	return buf.String(), nil
}

// parseWebhookTemplate mem-parsing template.
//
// missingkey=zero membuat key map yang tidak ada dirender sebagai nilai zero,
// bukan error, namun karena konteks pada file ini berupa struct, kegunaan
// utamanya adalah agar range pada .Items yang kosong tidak error. Yang
// benar-benar perlu dicegah adalah .Items yang nil.
func parseWebhookTemplate(tpl string) (*template.Template, error) {
	return template.New("body").Funcs(webhookTemplateFuncs).Option("missingkey=zero").Parse(tpl)
}

// webhookTemplateFuncs adalah fungsi bantu yang diekspos ke template.
var webhookTemplateFuncs = template.FuncMap{
	// json menyerialisasikan nilai apa pun menjadi JSON.
	//
	// Fungsi ini bukan sekadar pemanis melainkan wajib ada: tanpanya, pengguna
	// hanya bisa menulis {{.Title}} untuk interpolasi langsung, dan begitu judul
	// temuan mengandung tanda kutip atau baris baru, seluruh isi permintaan
	// langsung bukan JSON yang valid lagi. Penerima akan menolaknya, dengan
	// pesan error yang mengarah ke "gagal parsing JSON", tanpa ada petunjuk
	// bahwa sumbernya adalah tanda kutip di judul.
	"json": func(v any) (string, error) {
		raw, err := json.Marshal(v)
		if err != nil {
			return "", err
		}
		return string(raw), nil
	},
	// jsons dipakai untuk menyisipkan potongan JSON ke dalam nilai string JSON lain
	// (melakukan satu lapis escape string).
	"jsons": func(v any) (string, error) {
		raw, err := json.Marshal(v)
		if err != nil {
			return "", err
		}
		quoted, err := json.Marshal(string(raw))
		if err != nil {
			return "", err
		}
		// Menghapus tanda kutip di bagian luar: pemanggil sendiri yang menentukan
		// apakah perlu menambahkan tanda kutip atau tidak.
		return string(quoted[1 : len(quoted)-1]), nil
	},
}

func newWebhookTemplateData(m Message) webhookTemplateData {
	d := webhookTemplateData{
		Title:   markdownTitle(m),
		Batch:   m.Batch,
		Count:   len(m.Items),
		HomeURL: m.HomeURL,
		SentAt:  time.Now().Format(time.RFC3339),
		Items:   make([]webhookItem, 0, len(m.Items)),
	}
	for _, it := range m.Items {
		wi := webhookItem{
			FindingID:     it.FindingID,
			Name:          it.Name,
			VulnClass:     it.VulnClass,
			Severity:      it.Severity,
			SeverityLabel: SeverityLabel(it.Severity),
			Summary:       it.Summary,
			Assets:        append([]string{}, it.Assets...),
			DetailURL:     it.DetailURL,
			FromStatus:    it.FromStatus,
			ToStatus:      it.ToStatus,
		}
		if it.IsStatusChange() {
			wi.StatusLabel = StatusLabel(it.FromStatus) + " → " + StatusLabel(it.ToStatus)
		}
		d.Items = append(d.Items, wi)
	}
	return d
}
