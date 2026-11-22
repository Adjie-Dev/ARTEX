package notify

import (
	"context"
	"errors"
	"sort"
	"strconv"
	"strings"
)

// Channel adalah adapter untuk satu saluran notifikasi. Implementasinya harus
// **tanpa state**: satu instance yang sama akan dipakai bersamaan oleh beberapa
// konfigurasi saluran, kredensial selalu diteruskan lewat parameter cfg.
type Channel interface {
	// Kind mengembalikan identitas tipe saluran, harus sama dengan key di registry.
	Kind() string
	// Validate dipanggil saat konfigurasi disimpan, memvalidasi field wajib dan
	// formatnya. Error yang dikembalikan akan ditampilkan langsung ke orang yang
	// mengonfigurasi, jadi teksnya harus menjelaskan "field mana yang kurang",
	// bukan sekadar "konfigurasi tidak valid".
	Validate(cfg map[string]any) error
	// Send mengirim satu pesan, mengembalikan **jumlah entri yang benar-benar terkirim** beserta error.
	//
	// Alasan harus mengembalikan jumlah: setiap platform punya batas panjang
	// pesan, pesan gabungan yang tidak muat dalam satu batch akan dipotong. Kalau
	// pemanggil tanpa syarat menandai seluruh batch sebagai terkirim, entri yang
	// terpotong itu akan hilang begitu saja, tidak terlihat di pesan, riwayat
	// pengiriman pun menunjukkan sukses, sehingga tidak ada satu tempat pun yang
	// bisa mengungkap bahwa sebagian pesan tidak pernah terkirim. Setelah kept
	// dikembalikan, pemanggil hanya menandai kept entri pertama, sisanya menunggu batch berikutnya.
	//
	// Error yang dikembalikan berarti pengiriman gagal, dan *PermanentError
	// berarti tidak perlu di-retry. Saat gagal, nilai kept tidak berarti apa pun dan harus diabaikan oleh pemanggil.
	Send(ctx context.Context, cfg map[string]any, m Message) (int, error)
	// DefaultRatePerMin mengembalikan batas pengiriman per menit yang disarankan
	// resmi oleh saluran tersebut, dipakai sebagai nilai default rate limit saat
	// instance saluran baru dibuat. Mengembalikan 0 berarti tidak ada batas yang diketahui.
	DefaultRatePerMin() int
	// SecretKeys mengembalikan nama-nama key dalam konfigurasi saluran yang
	// termasuk kredensial. Saat ditampilkan lewat API, nilai key-key ini akan
	// disamarkan, dan saat update menerima nilai yang disamarkan, nilai asli di
	// database tetap dipertahankan. Hanya implementasi masing-masing yang tahu
	// field mana yang termasuk kredensial (seluruh alamat Webhook WeCom adalah
	// kredensial, sedangkan untuk DingTalk hanya bagian secret-nya saja), jadi
	// pengetahuan ini harus disediakan oleh saluran itu sendiri, tidak boleh ditebak oleh lapisan atasnya.
	SecretKeys() []string
	// DestinationKeys mengembalikan nama-nama key dalam konfigurasi saluran yang menentukan "pesan dikirim ke mana".
	//
	// Sama seperti SecretKeys, ini juga hal yang terkait keamanan: alamat tujuan
	// dan kredensial adalah dua kelompok field yang terpisah. Kalau diizinkan
	// "hanya mengubah alamat, kredensial tetap seperti semula", siapa pun yang
	// bisa mengubah konfigurasi saluran bisa mengarahkan kredensial asli di
	// database ke server yang mereka kendalikan sendiri, dan penyamaran
	// konfigurasi saluran jadi sama sekali tidak berarti.
	// Lihat PrepareConfigUpdate untuk detailnya.
	DestinationKeys() []string
}

// registry adalah registry saluran. Sengaja menggunakan literal eksplisit
// alih-alih self-registration lewat init(): dengan begini "saluran apa saja
// yang ada" bisa dilihat utuh di satu tempat, dan saluran baru yang terlupa
// didaftarkan akan ketahuan sejak waktu kompilasi, bukan lewat efek samping
// saat runtime.
var registry = map[string]Channel{
	KindDingTalk: dingTalkChannel{},
	KindFeishu:   feishuChannel{},
	KindWeCom:    weComChannel{},
	KindWebhook:  webhookChannel{},
	KindTelegram: telegramChannel{},
	KindEmail:    emailChannel{},
}

// Get mengambil implementasi saluran berdasarkan tipenya.
func Get(kind string) (Channel, bool) {
	c, ok := registry[kind]
	return c, ok
}

// ValidKind melaporkan apakah kind adalah tipe saluran yang didukung.
func ValidKind(kind string) bool {
	_, ok := registry[kind]
	return ok
}

// Kinds mengembalikan semua tipe saluran yang didukung, diurutkan secara alfabetis (agar dropdown di UI tampil konsisten).
func Kinds() []string {
	out := make([]string, 0, len(registry))
	for k := range registry {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

// PermanentError menandai sebuah kegagalan pengiriman yang tidak perlu
// di-retry: kredensial salah, ditolak oleh tujuan, body request tidak valid,
// dan sejenisnya. Retry hanya masuk akal untuk gangguan sesaat (jaringan
// tidak stabil, rate limit, 5xx dari sisi lawan); untuk kegagalan permanen,
// mencoba ulang berkali-kali dengan backoff tidak akan berhasil, dan malah
// menenggelamkan error yang sebenarnya di tumpukan log retry.
type PermanentError struct{ Err error }

func (e *PermanentError) Error() string { return e.Err.Error() }
func (e *PermanentError) Unwrap() error { return e.Err }

// Permanent menandai err sebagai kegagalan permanen. Mengembalikan nil kalau
// err adalah nil, supaya bisa ditulis sebagai `return Permanent(someCheck())`.
func Permanent(err error) error {
	if err == nil {
		return nil
	}
	return &PermanentError{Err: err}
}

// IsPermanent melaporkan apakah rantai err membawa tanda kegagalan permanen.
func IsPermanent(err error) bool {
	var pe *PermanentError
	return errors.As(err, &pe)
}

// ---- Helper pembacaan konfigurasi ----
//
// Konfigurasi saluran berasal dari kolom JSONB di database, setelah
// di-deserialize lewat encoding/json menjadi map[string]any, dengan angka
// selalu berupa float64 dan array berupa []any. Helper-helper di bawah ini
// menyatukan konversi tersebut, dan juga menoleransi penyimpangan tipe akibat
// input pengguna di UI (misalnya mengisi port sebagai string).

// cfgString mengambil item konfigurasi bertipe string, spasi di awal/akhir selalu dipotong, karena copy-paste dari form web mudah membawa spasi tambahan.
func cfgString(cfg map[string]any, key string) string {
	v, ok := cfg[key]
	if !ok {
		return ""
	}
	s, ok := v.(string)
	if !ok {
		return ""
	}
	return strings.TrimSpace(s)
}

// cfgInt mengambil item konfigurasi bertipe integer, mendukung dua sumber: float64 (default JSON) dan string.
func cfgInt(cfg map[string]any, key string) int {
	switch v := cfg[key].(type) {
	case float64:
		return int(v)
	case int:
		return v
	case string:
		n, err := strconv.Atoi(strings.TrimSpace(v))
		if err != nil {
			return 0
		}
		return n
	default:
		return 0
	}
}

// cfgBool mengambil item konfigurasi bertipe boolean, mendukung string "true"/"1".
func cfgBool(cfg map[string]any, key string) bool {
	switch v := cfg[key].(type) {
	case bool:
		return v
	case string:
		s := strings.ToLower(strings.TrimSpace(v))
		return s == "true" || s == "1" || s == "yes"
	default:
		return false
	}
}

// cfgStrings mengambil item konfigurasi berupa array string, otomatis memotong spasi dan membuang string kosong.
func cfgStrings(cfg map[string]any, key string) []string {
	raw, ok := cfg[key].([]any)
	if !ok {
		// Juga menerima satu string tunggal, agar submit form dengan satu nilai saja tetap mudah.
		if s := cfgString(cfg, key); s != "" {
			return []string{s}
		}
		return nil
	}
	out := make([]string, 0, len(raw))
	for _, v := range raw {
		s, ok := v.(string)
		if !ok {
			continue
		}
		if s = strings.TrimSpace(s); s != "" {
			out = append(out, s)
		}
	}
	return out
}

// cfgMap mengambil item konfigurasi berupa map string (misalnya header HTTP custom), key dan value sama-sama dipotong spasinya, key kosong dibuang.
func cfgMap(cfg map[string]any, key string) map[string]string {
	raw, ok := cfg[key].(map[string]any)
	if !ok {
		return nil
	}
	out := make(map[string]string, len(raw))
	for k, v := range raw {
		k = strings.TrimSpace(k)
		if k == "" {
			continue
		}
		s, ok := v.(string)
		if !ok {
			continue
		}
		out[k] = s
	}
	return out
}
