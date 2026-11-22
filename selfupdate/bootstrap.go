package selfupdate

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"log"
	"os"
	"os/exec"
	"strings"
	"time"
)

// smokeEnv membuat proses anak yang dijalankan oleh smoke test langsung melewati Bootstrap.
//
// Sebenarnya tanpa ini pun tidak akan jadi masalah: os.Executable() milik proses
// anak adalah artex.new, sehingga semua path yang diturunkan darinya memakai
// prefix .new dan tidak akan menyentuh file upgrade yang sebenarnya. Tapi
// mengandalkan kebetulan seperti itu terlalu rapuh, jadi di sini dibuat jalan
// pintas yang eksplisit dan jelas, sekaligus menghemat satu kali pengecekan disk
// yang sia-sia pada proses anak.
const smokeEnv = "ARTEX_SELFUPDATE_SMOKE"

// Action adalah instruksi yang diberikan Bootstrap ke main.
type Action int

const (
	// Continue: lanjutkan start server seperti biasa.
	Continue Action = iota
	// Restart: langsung keluar dengan ExitRestart, supaya script penjaga menjalankannya ulang.
	Restart
)

// State menggambarkan status upgrade pada saat start ini, dipakai oleh
// /api/update/check untuk memberi tahu frontend secara jujur apakah upgrade
// terakhir berhasil atau malah di-rollback.
type State struct {
	Pending     bool   // sudah diganti versi tapi belum dikonfirmasi stabil
	RolledBack  bool   // baru saja menjalankan rollback otomatis pada start ini
	FailedStage bool   // verifikasi/smoke test file staging gagal, sudah dibuang
	Detail      string // penjelasan satu kalimat untuk ditampilkan ke pengguna
}

// Bootstrap dijalankan di paling awal main, harus dipanggil sebelum membuka port
// listener atau database apa pun.
//
// Ada tiga kemungkinan kondisi:
//
//	1. Ada file staging artex.new  -> verifikasi + smoke test, kalau lolos diganti
//	   versinya dan minta restart; kalau tidak lolos, dibuang dan lanjut jalan
//	   dengan versi lama
//	2. Hanya tersisa file marker   -> berarti baru saja selesai ganti versi,
//	   tambahkan satu hitungan percobaan; kalau gagal berturut-turut sampai batas,
//	   di-rollback
//	3. Tidak ada apa-apa           -> start normal
func Bootstrap() (Action, State) {
	if os.Getenv(smokeEnv) != "" {
		return Continue, State{}
	}
	p, err := ResolvePaths()
	if err != nil {
		log.Printf("[update] melewati bootstrap: %v", err)
		return Continue, State{}
	}

	if _, err := os.Stat(p.New); err == nil {
		return applyStaged(p)
	}

	m, ok := readMarker(p.Marker)
	if !ok {
		return Continue, State{}
	}
	return confirmOrRollback(p, m)
}

// applyStaged menangani kondisi "ada file staging": kalau lolos verifikasi
// langsung diganti versinya, kalau gagal dibuang.
//
// Ini adalah satu-satunya tempat di seluruh jalur upgrade yang menimpa file
// executable, dan juga gerbang terakhir, smoke test menahan masalah seperti
// download yang rusak, salah pilih arsitektur, atau dynamic link yang hilang.
// Kalau sampai ada binary yang tidak bisa jalan diloloskan, script penjaga akan
// terus-menerus mencoba menjalankannya ulang tanpa henti, sementara kode Go
// sama sekali tidak kebagian kesempatan untuk berjalan, sehingga rollback
// otomatis pun jadi tidak mungkin terjadi.
func applyStaged(p Paths) (Action, State) {
	m, _ := readMarker(p.Marker)

	if err := verifyStaged(p); err != nil {
		log.Printf("[update] versi baru yang di-staging tidak lolos verifikasi, dibuang, lanjut menjalankan versi saat ini: %v", err)
		cleanStaged(p)
		_ = os.Remove(p.Marker)
		return Continue, State{FailedStage: true, Detail: "Verifikasi versi baru gagal, sudah dibuang: " + err.Error()}
	}

	if err := swap(p); err != nil {
		log.Printf("[update] gagal mengganti versi, lanjut menjalankan versi saat ini: %v", err)
		cleanStaged(p)
		_ = os.Remove(p.Marker)
		return Continue, State{FailedStage: true, Detail: "Gagal mengganti versi: " + err.Error()}
	}

	// Pergantian versi berhasil. Marker dipertahankan, diserahkan ke start
	// berikutnya (yang sudah menjalankan versi baru) untuk mengonfirmasi apakah stabil.
	m.Attempts = 0
	if m.StagedAt == 0 {
		m.StagedAt = time.Now().Unix()
	}
	if err := writeMarker(p.Marker, m); err != nil {
		log.Printf("[update] gagal menulis marker upgrade (kemampuan rollback otomatis hilang): %v", err)
	}
	log.Printf("[update] sudah diganti ke versi %s, keluar untuk restart (exit %d)", orUnknown(m.To), ExitRestart)
	return Restart, State{Pending: true}
}

// confirmOrRollback menangani "start setelah pergantian versi": menghitung
// jumlah percobaan, kalau sudah melewati batas maka versi lama dikembalikan.
//
// Hitungan ini baru bertambah setelah kode Go sempat berjalan, sehingga yang
// ter-cover adalah kegagalan jenis "bisa dieksekusi tapi crash saat inisialisasi"
// (konfigurasi tidak kompatibel, port terpakai, migrasi DB gagal); kegagalan
// jenis "sama sekali tidak bisa di-exec" sudah ditahan oleh smoke test sebelum
// pergantian versi, gabungan keduanya baru benar-benar lengkap.
func confirmOrRollback(p Paths, m marker) (Action, State) {
	m.Attempts++
	if m.Attempts > maxAttempts {
		if err := rollback(p); err != nil {
			// Kalau rollback pun gagal, jangan restart lagi, supaya tidak terjebak
			// restart tanpa henti. Marker dihapus, biarkan proses berjalan dengan
			// kondisi saat ini, kalau pun tidak bisa jalan, setidaknya pengguna
			// masih bisa melihat alasannya di log.
			log.Printf("[update] versi baru gagal start %d kali berturut-turut, dan rollback juga gagal: %v", maxAttempts, err)
			_ = os.Remove(p.Marker)
			return Continue, State{Detail: "Versi baru gagal start dan rollback juga gagal: " + err.Error()}
		}
		log.Printf("[update] versi baru gagal start %d kali berturut-turut, sudah di-rollback ke %s, keluar untuk restart (exit %d)",
			maxAttempts, orUnknown(m.From), ExitRestart)
		_ = os.Remove(p.Marker)
		return Restart, State{RolledBack: true, Detail: fmt.Sprintf("Versi baru gagal start, sudah di-rollback ke %s", orUnknown(m.From))}
	}
	if err := writeMarker(p.Marker, m); err != nil {
		log.Printf("[update] gagal memperbarui marker upgrade: %v", err)
	}
	log.Printf("[update] versi baru sedang start (percobaan ke-%d/%d), upgrade akan dikonfirmasi setelah berjalan stabil",
		m.Attempts, maxAttempts)
	return Continue, State{Pending: true}
}

// Settle mengonfirmasi bahwa versi baru sudah berjalan stabil, lalu menghapus marker upgrade.
//
// Dipanggil oleh main dengan delay setelah HTTP listener aktif: baru dianggap
// berhasil kalau bisa bertahan selama periode ini, kalau tidak marker tetap ada
// di tempatnya dan start berikutnya akan terus menambah hitungan percobaan
// sampai memicu rollback.
func Settle() {
	p, err := ResolvePaths()
	if err != nil {
		return
	}
	settle(p)
}

func settle(p Paths) {
	if _, ok := readMarker(p.Marker); !ok {
		return // bukan start setelah upgrade, tidak ada yang perlu dilakukan
	}
	if err := os.Remove(p.Marker); err != nil && !errors.Is(err, os.ErrNotExist) {
		log.Printf("[update] gagal menghapus marker upgrade: %v", err)
		return
	}
	log.Printf("[update] versi baru berjalan stabil, upgrade selesai (versi sebelumnya disimpan sebagai %s)", p.Old)
}

// SettleDelay adalah lama waktu berjalan yang dibutuhkan untuk menentukan "versi baru berhasil bertahan".
const SettleDelay = 30 * time.Second

// verifyStaged memverifikasi file staging: bandingkan SHA256 dulu, baru benar-benar menjalankannya sekali.
func verifyStaged(p Paths) error {
	want, err := os.ReadFile(p.Sum)
	if err != nil {
		return fmt.Errorf("membaca checksum: %w", err)
	}
	got, err := fileSHA256(p.New)
	if err != nil {
		return fmt.Errorf("menghitung checksum: %w", err)
	}
	if !strings.EqualFold(strings.TrimSpace(string(want)), got) {
		return errors.New("SHA256 tidak cocok (download rusak atau dimanipulasi)")
	}
	return smokeTest(p.New)
}

// smokeTest menjalankan binary baru dengan -h untuk memastikan binary itu benar-benar bisa dieksekusi di sistem ini.
// Ini menahan masalah seperti download yang terpotong, salah pilih arsitektur (exec format error), dependency yang hilang, dan sejenisnya.
func smokeTest(bin string) error {
	if err := os.Chmod(bin, 0o755); err != nil {
		return fmt.Errorf("memberi izin eksekusi: %w", err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	cmd := exec.CommandContext(ctx, bin, "-h")
	cmd.Env = append(os.Environ(), smokeEnv+"=1")
	out, err := cmd.CombinedOutput()
	if ctx.Err() != nil {
		return errors.New("smoke test timeout (binary baru tidak merespons)")
	}
	if err != nil {
		snippet := strings.TrimSpace(string(out))
		if len(snippet) > 300 {
			snippet = snippet[:300] + "…"
		}
		return fmt.Errorf("smoke test gagal: %v: %s", err, snippet)
	}
	return nil
}

// swap mengganti binary saat ini dengan versi baru yang di-staging.
//
// Unix maupun Windows sama-sama mengizinkan rename pada executable yang sedang
// berjalan (yang dilarang di Windows adalah delete dan overwrite, rename tidak
// termasuk), jadi di sini tidak perlu dibedakan per platform, dan juga tidak
// perlu menghentikan diri sendiri dulu.
func swap(p Paths) error {
	// rename di Windows tidak akan menimpa target yang sudah ada, jadi file .old
	// sisa dari upgrade sebelumnya harus dibersihkan dulu.
	if err := os.Remove(p.Old); err != nil && !errors.Is(err, os.ErrNotExist) {
		return fmt.Errorf("membersihkan backup lama %s: %w", p.Old, err)
	}
	if err := os.Rename(p.Current, p.Old); err != nil {
		return fmt.Errorf("membackup versi saat ini: %w", err)
	}
	if err := os.Rename(p.New, p.Current); err != nil {
		// Pergantian versi gagal, tetapi versi saat ini sudah terlanjur dipindah,
		// harus dikembalikan seperti semula, kalau tidak start berikutnya tidak
		// akan punya file executable.
		if rerr := os.Rename(p.Old, p.Current); rerr != nil {
			return fmt.Errorf("gagal memasang versi baru (%v), dan gagal juga mengembalikan versi saat ini: %w", err, rerr)
		}
		return fmt.Errorf("memasang versi baru: %w", err)
	}
	_ = os.Remove(p.Sum)
	return nil
}

// rollback mengembalikan versi lama yang dibackup oleh swap.
func rollback(p Paths) error {
	if _, err := os.Stat(p.Old); err != nil {
		return fmt.Errorf("tidak ada backup %s yang bisa di-rollback: %w", p.Old, err)
	}
	// Versi baru yang gagal jalan dipindah ke .failed untuk keperluan penelusuran, bukan langsung dihapus.
	failed := p.Current + ".failed"
	_ = os.Remove(failed)
	if err := os.Rename(p.Current, failed); err != nil {
		return fmt.Errorf("memindahkan versi yang gagal: %w", err)
	}
	if err := os.Rename(p.Old, p.Current); err != nil {
		return fmt.Errorf("mengembalikan versi lama: %w", err)
	}
	return nil
}

// Rollback adalah implementasi dari /api/update/rollback: secara aktif mengembalikan ke versi sebelumnya.
// Hanya melakukan pergantian versi, restart tetap diserahkan ke script penjaga (pemanggil keluar dengan ExitRestart setelah ini).
func Rollback() error {
	p, err := ResolvePaths()
	if err != nil {
		return err
	}
	if _, err := os.Stat(p.Old); err != nil {
		return errors.New("tidak ada versi sebelumnya yang bisa di-rollback (" + p.Old + " tidak ditemukan)")
	}
	cleanStaged(p)
	if err := smokeTest(p.Old); err != nil {
		return fmt.Errorf("versi sebelumnya tidak bisa dieksekusi, rollback dibatalkan: %w", err)
	}
	// Menukar versi saat ini dengan backup: setelah rollback masih bisa di-rollback lagi.
	tmp := p.Current + ".swap"
	_ = os.Remove(tmp)
	if err := os.Rename(p.Current, tmp); err != nil {
		return fmt.Errorf("memindahkan versi saat ini: %w", err)
	}
	if err := os.Rename(p.Old, p.Current); err != nil {
		_ = os.Rename(tmp, p.Current)
		return fmt.Errorf("memasang versi sebelumnya: %w", err)
	}
	if err := os.Rename(tmp, p.Old); err != nil {
		log.Printf("[update] gagal merapikan backup setelah rollback (tidak memengaruhi operasi): %v", err)
	}
	_ = os.Remove(p.Marker)
	return nil
}

// HasBackup melaporkan apakah ada versi sebelumnya yang bisa di-rollback, dipakai frontend untuk menentukan apakah tombol rollback perlu ditampilkan.
func HasBackup() bool {
	p, err := ResolvePaths()
	if err != nil {
		return false
	}
	_, err = os.Stat(p.Old)
	return err == nil
}

func fileSHA256(path string) (string, error) {
	f, err := os.Open(path)
	if err != nil {
		return "", err
	}
	defer f.Close()
	h := sha256.New()
	if _, err := io.Copy(h, f); err != nil {
		return "", err
	}
	return hex.EncodeToString(h.Sum(nil)), nil
}

func orUnknown(s string) string {
	if strings.TrimSpace(s) == "" {
		return "versi tidak diketahui"
	}
	return s
}
