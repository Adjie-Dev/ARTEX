package notify

import (
	"encoding/json"
	"fmt"
	"slices"
	"strings"
)

// Filter adalah kontrak untuk kolom JSONB notification_channels.filter:
// kondisi filter milik instance channel.
// Semua field bersifat opsional, nilai default berarti "tidak difilter",
// inilah makna fallback untuk konfigurasi yang rusak, lihat ParseFilter.
type Filter struct {
	// MinSeverity adalah ambang level minimum (low/medium/high/critical),
	// kosong berarti tidak ada ambang batas.
	MinSeverity string `json:"min_severity"`
	// TaskIDs / AssetIDs: array kosong berarti tidak dibatasi; jika tidak
	// kosong, event harus beririsan dengan daftar ini.
	TaskIDs  []int64 `json:"task_ids"`
	AssetIDs []int64 `json:"asset_ids"`
	// VulnClassInclude kosong berarti menerima semua; jika tidak kosong,
	// vulnclass harus cocok dengan salah satu kata kunci di dalamnya.
	// VulnClassExclude: cocok dengan salah satu kata kunci berarti dikecualikan
	// (exclude lebih diutamakan daripada include).
	// Pencocokan berupa substring yang tidak case-sensitive, lebih aman
	// dibanding regex: kesalahan menulis regex oleh pengguna tidak akan
	// membuat channel gagal secara diam-diam.
	VulnClassInclude []string `json:"vulnclass_include"`
	VulnClassExclude []string `json:"vulnclass_exclude"`
	// OnStatusChange menentukan apakah channel ini menerima event perubahan
	// status kerentanan (hanya relevan pada mode realtime).
	OnStatusChange bool `json:"on_status_change"`
}

// ParseFilter mem-parsing konfigurasi filter channel.
//
// **Tidak pernah mengembalikan error.** Ini adalah pilihan desain yang
// disengaja: ketika konfigurasi filter rusak, selalu diturunkan menjadi
// Filter kosong (= tidak difilter = semua event cocok), karena untuk sistem
// notifikasi kerentanan, **lebih baik mengirim notifikasi berlebih daripada
// diam-diam melewatkan satu kerentanan berbahaya**. Membuat parsing yang
// gagal menjadi "tidak mengirim" sama saja memberi pengguna channel yang
// terlihat sudah terkonfigurasi tapi sebenarnya tidak pernah mengirim apa
// pun, ini adalah mode kegagalan yang paling buruk.
func ParseFilter(raw []byte) Filter {
	var f Filter
	if len(raw) == 0 {
		return f
	}
	// Jika parsing gagal, f tetap berupa nilai kosong, artinya tidak difilter.
	_ = json.Unmarshal(raw, &f)
	return f
}

// ValidMinSeverity melaporkan apakah s adalah ambang level yang valid
// (string kosong berarti tidak ada ambang batas).
func ValidMinSeverity(s string) bool {
	if s == "" {
		return true
	}
	_, ok := severityRank[s]
	return ok
}

// Validate memvalidasi field-field konfigurasi filter yang **nilainya
// terbatas**, dipanggil saat menyimpan channel.
//
// Alasan validasi ini wajib dilakukan saat menulis: Match menilai ambang
// batas yang tidak dikenal dengan `rank >= 0`, yang selalu benar, artinya
// kalau min_severity salah ketik (misalnya "hgih"), filter akan **gagal
// secara diam-diam** dan berubah jadi "kirim semua". Ini sejalan dengan
// prinsip paket ini "lebih baik kirim lebih daripada kelewat" (tidak akan
// kelewat), tapi akibatnya pengguna mengira sedang melakukan pengiriman
// bertingkat padahal sebenarnya semua kerentanan dikirim ke grup, dan tidak
// ada tanda apa pun bahwa konfigurasinya salah. Jenis "degradasi diam-diam"
// seperti ini seharusnya memang dicegah sejak awal.
//
// Perhatikan, Validate hanya dipakai pada jalur **penulisan**. Jalur
// pembacaan tetap memakai semantik toleran dari ParseFilter, sehingga nilai
// buruk yang sudah ada di data lama tidak membuat channel gagal dibaca sama
// sekali.
func (f Filter) Validate() error {
	if !ValidMinSeverity(f.MinSeverity) {
		return fmt.Errorf("level minimum %q tidak valid, pilihan: low / medium / high / critical, atau biarkan kosong untuk tidak membatasi", f.MinSeverity)
	}
	return nil
}

// Match menentukan apakah sebuah event harus dikirim ke channel dengan
// kondisi filter ini.
//
// **Tidak pernah mengembalikan error**, alasannya sama seperti ParseFilter:
// setiap anomali internal diperlakukan sebagai "cocok".
// Urutan pemeriksaan: jenis event -> ambang level -> ruang lingkup task/asset
// -> kata kunci jenis kerentanan.
func Match(f Filter, s Snapshot) bool {
	// Event perubahan status hanya diterima oleh channel yang secara eksplisit
	// mengaktifkannya. Default-nya nonaktif, karena kebanyakan pengguna
	// berharap "notifikasi" berarti "ditemukan kerentanan baru", bukan
	// mencatat setiap perubahan status satu per satu.
	if s.Kind == EventFindingStatusChanged && !f.OnStatusChange {
		return false
	}
	if !AtLeast(s.Severity, f.MinSeverity) {
		return false
	}
	if len(f.TaskIDs) > 0 && !slices.Contains(f.TaskIDs, s.TaskID) {
		return false
	}
	if len(f.AssetIDs) > 0 && !intersectsInt(f.AssetIDs, s.AssetIDs) {
		return false
	}
	// Exclude diutamakan: cocok dengan salah satu kata kunci exclude langsung
	// tersingkir, meskipun juga cocok dengan daftar include.
	if len(f.VulnClassExclude) > 0 && containsAnyFold(s.VulnClass, f.VulnClassExclude) {
		return false
	}
	if len(f.VulnClassInclude) > 0 && !containsAnyFold(s.VulnClass, f.VulnClassInclude) {
		return false
	}
	return true
}

func intersectsInt(a, b []int64) bool {
	// Cukup pindai linear untuk kumpulan kecil; skala di kedua sisi sama-sama
	// "beberapa puluh item yang dipilih manual", membuat map justru lebih
	// mahal daripada manfaatnya.
	for _, v := range b {
		if slices.Contains(a, v) {
			return true
		}
	}
	return false
}

// containsAnyFold melaporkan apakah s mengandung salah satu kata kunci di
// keywords (tidak case-sensitive).
func containsAnyFold(s string, keywords []string) bool {
	lower := strings.ToLower(s)
	for _, kw := range keywords {
		kw = strings.ToLower(strings.TrimSpace(kw))
		if kw != "" && strings.Contains(lower, kw) {
			return true
		}
	}
	return false
}
