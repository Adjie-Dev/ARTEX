// Package selfupdate mengimplementasikan update satu klik dari halaman ARTEX: mengambil binary versi baru dari GitHub Release,
// memverifikasinya, menyimpannya sementara, dan menggantinya secara atomik saat startup berikutnya.
//
// Pembagian kerja secara keseluruhan (lihat start.sh / start.bat):
//
//	Skrip startup = loop penjaga sederhana, hanya bertugas "setelah proses keluar, memutuskan apakah dijalankan ulang berdasarkan exit code"
//	Package ini  = seluruh logika yang rawan error (download / verifikasi SHA256 / smoke test / penggantian / rollback saat gagal)
//
// Alasan penggantian binary dilakukan di Go, bukan di skrip, adalah karena verifikasi sha256 dan smoke test di sh dan bat
// harus ditulis dua kali (sha256sum / shasum / certutil), padahal bagian inilah yang paling tidak boleh salah, kalau sampai
// mengganti dengan binary yang tidak bisa jalan, proses penjaga akan setia menjalankannya ulang terus-menerus, dan pengguna hanya bisa memperbaikinya manual di server.
//
// Satu kali upgrade lengkap melewati tiga kali startup proses:
//
//	① Server versi lama menerima /api/update/apply, download dan verifikasi, simpan sementara sebagai artex.new, exit 75
//	② Skrip menjalankan ulang versi lama, Bootstrap mendeteksi artex.new, verifikasi + smoke test, ganti binary, exit 75
//	③ Skrip menjalankan ulang, saat ini sudah versi baru, Bootstrap mencatat satu percobaan, tanda dihapus setelah startup berhasil
//
// Kegagalan di langkah manapun akan kembali ke versi lama: pada tahap kedua, jika verifikasi gagal, file sementara dihapus dan versi lama terus berjalan; pada tahap ketiga, jika gagal mencapai
// penghapusan tanda sebanyak 3 kali berturut-turut (gagal hidup = crash), artex.old akan otomatis dikembalikan.
package selfupdate

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
)

// ExitRestart adalah exit code "minta proses penjaga menjalankan saya ulang" (EX_TEMPFAIL). Begitu skrip startup melihatnya,
// proses langsung dijalankan ulang tanpa dihitung sebagai backoff crash. 0 berarti pengguna menghentikan secara normal (skrip keluar dari loop), selain itu dianggap crash.
const ExitRestart = 75

// maxAttempts adalah jumlah percobaan startup yang diizinkan setelah penggantian binary. Setiap startup versi baru akan menambah hitungan +1, jika berhasil hidup melewati
// settleDelay maka tandanya dihapus; jika crash berturut-turut sebanyak maxAttempts kali berarti versi baru memang tidak bisa jalan, dan akan di-rollback otomatis.
const maxAttempts = 3

// Paths adalah seluruh file yang terlibat dalam satu kali upgrade, semuanya diletakkan di **direktori tempat file executable berada**.
// Sengaja tidak memakai CWD: saat berjalan sebagai service, working directory bisa jadi / atau path apa saja, memakai CWD akan membuat file sementara tersimpan
// di tempat lain, sehingga logika penggantian langsung gagal berfungsi.
type Paths struct {
	Dir     string // Direktori tempat file executable berada
	Current string // Binary yang sedang berjalan        artex      / artex.exe
	New     string // Versi baru yang disimpan sementara  artex.new  / artex.new.exe
	Sum     string // sha256 (hex) versi baru  artex.new.sha256 / artex.new.exe.sha256
	Old     string // Backup versi lama sebelum diganti   artex.old  / artex.old.exe
	Marker  string // Tanda status upgrade                artex.upgrade.json
}

// ResolvePaths menurunkan seluruh path upgrade berdasarkan file executable saat ini.
//
// Di Windows, .new/.old juga harus memakai akhiran .exe, kalau tidak smoke test dan eksekusi setelah penggantian akan gagal,
// sehingga akhirannya dilepas dulu baru digabung kembali, agar penamaan di kedua platform tetap simetris.
func ResolvePaths() (Paths, error) {
	exe, err := os.Executable()
	if err != nil {
		return Paths{}, fmt.Errorf("gagal menentukan lokasi file executable: %w", err)
	}
	if resolved, err := filepath.EvalSymlinks(exe); err == nil {
		exe = resolved
	}
	dir := filepath.Dir(exe)
	name := filepath.Base(exe)
	ext := filepath.Ext(name) // Di Windows adalah ".exe", di Unix biasanya kosong
	stem := strings.TrimSuffix(name, ext)

	join := func(suffix string) string { return filepath.Join(dir, stem+suffix+ext) }
	return Paths{
		Dir:     dir,
		Current: exe,
		New:     join(".new"),
		Sum:     join(".new") + ".sha256",
		Old:     join(".old"),
		Marker:  filepath.Join(dir, stem+".upgrade.json"),
	}, nil
}

// marker mencatat progres satu kali penggantian binary, dipakai untuk memicu rollback otomatis saat versi baru gagal hidup.
type marker struct {
	From     string `json:"from"`     // Versi sebelum upgrade
	To       string `json:"to"`       // Versi tujuan
	Attempts int    `json:"attempts"` // Jumlah percobaan startup setelah penggantian binary
	StagedAt int64  `json:"staged_at"`
}

func readMarker(path string) (marker, bool) {
	b, err := os.ReadFile(path)
	if err != nil {
		return marker{}, false
	}
	var m marker
	if json.Unmarshal(b, &m) != nil {
		return marker{}, false
	}
	return m, true
}

func writeMarker(path string, m marker) error {
	b, err := json.MarshalIndent(m, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(path, b, 0o644)
}

// cleanStaged menghapus file yang disimpan sementara. Dipanggil saat penggantian berhasil, verifikasi gagal, atau dibatalkan pengguna, agar tidak ada sisa
// artex.new yang dicoba lagi pada startup berikutnya.
func cleanStaged(p Paths) {
	_ = os.Remove(p.New)
	_ = os.Remove(p.Sum)
}

// CompareVersions membandingkan dua nomor versi, mengembalikan -1/0/1 (a<b / a==b / a>b).
// ok=false berarti setidaknya satu sisi bukan nomor versi yang bisa dibandingkan (misalnya "dev" dari build pengembangan lokal, atau
// "0.3.7-2-gabc1234-dirty" hasil git describe), dalam kasus ini pemanggil harus menonaktifkan update satu klik,
// kalau tidak build pengembangan bisa ter-"upgrade" jadi versi resmi dan menimpa perubahan yang belum di-commit.
func CompareVersions(a, b string) (int, bool) {
	av, aok := parseVersion(a)
	bv, bok := parseVersion(b)
	if !aok || !bok {
		return 0, false
	}
	for i := range 3 {
		if av[i] != bv[i] {
			if av[i] < bv[i] {
				return -1, true
			}
			return 1, true
		}
	}
	return 0, true
}

// parseVersion mem-parsing nomor versi berformat "v0.3.7" / "0.3.7" menjadi [3]int.
//
// Hanya menerima format tiga bagian murni: build.sh saat build non-tag memakai git describe yang menghasilkan
// versi berakhiran seperti "0.3.7-2-gabc1234", versi semacam ini harus dianggap tidak bisa dibandingkan, bukan dianggap sebagai
// 0.3.7, kalau tidak build pengembangan bisa salah dikira "sudah versi terbaru" atau ditimpa oleh versi resmi.
func parseVersion(s string) ([3]int, bool) {
	s = strings.TrimSpace(s)
	s = strings.TrimPrefix(s, "v")
	if s == "" {
		return [3]int{}, false
	}
	parts := strings.Split(s, ".")
	if len(parts) != 3 {
		return [3]int{}, false
	}
	var out [3]int
	for i, p := range parts {
		n, err := strconv.Atoi(p)
		if err != nil || n < 0 {
			return [3]int{}, false
		}
		out[i] = n
	}
	return out, true
}

// InDocker melaporkan apakah proses berjalan di dalam container. Di Docker, penggantian binary menulis ke writable layer container,
// membuat ulang container dengan `docker compose up -d` akan mengembalikan ke versi bawaan image, ini adalah perilaku yang memang diharapkan
// (karena saat itu pengguna memang sedang menarik image baru), tapi frontend harus bisa menjelaskan hal ini dengan jelas kepada pengguna.
func InDocker() bool {
	if _, err := os.Stat("/.dockerenv"); err == nil {
		return true
	}
	b, err := os.ReadFile("/proc/1/cgroup")
	if err != nil {
		return false
	}
	s := string(b)
	return strings.Contains(s, "docker") || strings.Contains(s, "containerd")
}
