package selfupdate

import (
	"archive/zip"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"net/http"
	"os"
	"path"
	"runtime"
	"strings"
	"time"
)

// sumsAsset adalah daftar checksum yang dihasilkan release.yml, mencakup semua zip di dalam Release.
const sumsAsset = "SHA256SUMS"

// maxBinarySize membatasi ukuran biner hasil ekstraksi, untuk mencegah zip yang cacat memenuhi disk.
const maxBinarySize = 512 << 20 // 512 MiB

// Phase adalah tahapan dalam proses upgrade, dipakai langsung sebagai field phase pada event SSE.
type Phase string

const (
	PhaseIdle     Phase = "idle"
	PhaseDownload Phase = "downloading"
	PhaseVerify   Phase = "verifying"
	PhaseExtract  Phase = "extracting"
	PhaseStaged   Phase = "staged"
	PhaseFailed   Phase = "failed"
)

// Progress disediakan oleh pemanggil, dipakai untuk mengirim progres ke frontend. pct hanya berarti pada tahap
// download (0-100), tahap lainnya mengirim -1.
type Progress func(ph Phase, pct int, msg string)

// Stage mengunduh paket rilis platform saat ini dari Release yang ditentukan, memvalidasinya, lalu menyimpan
// biner baru sebagai artex.new.
//
// Yang diunduh adalah zip lengkap, bukan biner mentah, karena dua alasan: SHA256SUMS pada Release yang sudah ada
// memang hanya mencakup zip, sehingga memakai zip tidak perlu mengubah CI dan tetap kompatibel dengan versi lama
// yang sudah dirilis; zip juga membawa skills/, yang membuka jalan untuk sinkronisasi skill bawaan di masa depan.
// Konsekuensinya hanya mengunduh beberapa ratus KB tambahan untuk skills.
//
// Fungsi ini mengembalikan nilai begitu proses penyimpanan selesai, lalu pemanggil menutup proses secara rapi dan keluar dengan ExitRestart.
func Stage(ctx context.Context, c *http.Client, rel *Release, currentVersion string, prog Progress) error {
	if prog == nil {
		prog = func(Phase, int, string) {}
	}
	p, err := ResolvePaths()
	if err != nil {
		return err
	}
	if err := checkWritable(p.Dir); err != nil {
		return err
	}

	name := AssetName(rel.TagName, runtime.GOOS, runtime.GOARCH)
	asset, ok := rel.FindAsset(name)
	if !ok {
		return fmt.Errorf("versi ini tidak menyediakan paket rilis untuk %s/%s (file %s tidak ada)", runtime.GOOS, runtime.GOARCH, name)
	}

	prog(PhaseDownload, 0, "Mengambil daftar checksum...")
	sums, err := fetchSums(ctx, c, rel)
	if err != nil {
		return err
	}
	want, ok := sums[name]
	if !ok {
		return fmt.Errorf("%s tidak mencantumkan %s, menolak memasang biner yang belum tervalidasi", sumsAsset, name)
	}

	// Semua file sementara disimpan di direktori target, agar rename terakhir menjadi operasi atomik di dalam
	// sistem file yang sama (rename antar-device akan gagal, dan /tmp sering menjadi mount point tersendiri).
	zipPath := p.New + ".zip.part"
	binPath := p.New + ".part"
	defer func() {
		_ = os.Remove(zipPath)
		_ = os.Remove(binPath)
	}()

	prog(PhaseDownload, 0, fmt.Sprintf("Mengunduh %s (%s)...", name, humanSize(asset.Size)))
	got, err := download(ctx, c, asset, zipPath, prog)
	if err != nil {
		return err
	}

	prog(PhaseVerify, -1, "Memverifikasi SHA256...")
	if !strings.EqualFold(got, want) {
		return fmt.Errorf("SHA256 tidak cocok: diharapkan %s, didapat %s (unduhan rusak atau telah diubah)", short(want), short(got))
	}

	prog(PhaseExtract, -1, "Mengekstrak dan menjalankan smoke test...")
	if err := extractBinary(zipPath, binPath); err != nil {
		return err
	}
	if err := smokeTest(binPath); err != nil {
		return fmt.Errorf("versi baru tidak dapat berjalan di sistem ini: %w", err)
	}

	// SHA256 dari file staging disimpan terpisah: saat startup berikutnya sebelum penggantian biner dilakukan,
	// akan divalidasi ulang sekali lagi, untuk mencegah file diubah atau rusak di rentang waktu antara staging dan restart.
	binSum, err := fileSHA256(binPath)
	if err != nil {
		return fmt.Errorf("menghitung checksum biner baru: %w", err)
	}
	if err := os.WriteFile(p.Sum, []byte(binSum), 0o644); err != nil {
		return fmt.Errorf("menulis checksum: %w", err)
	}
	if err := os.Rename(binPath, p.New); err != nil {
		_ = os.Remove(p.Sum)
		return fmt.Errorf("menyimpan versi baru: %w", err)
	}

	if err := writeMarker(p.Marker, marker{
		From:     currentVersion,
		To:       strings.TrimPrefix(rel.TagName, "v"),
		StagedAt: time.Now().Unix(),
	}); err != nil {
		// Marker hanya mempengaruhi kemampuan rollback otomatis, file staging sendiri sudah siap, sehingga upgrade tidak dibatalkan karena ini.
		prog(PhaseStaged, -1, "Peringatan: gagal menulis marker upgrade, upgrade kali ini tidak memiliki perlindungan rollback otomatis")
	}

	prog(PhaseStaged, 100, "Versi baru sudah siap, sedang memulai ulang...")
	return nil
}

// fetchSums mengunduh dan mengurai SHA256SUMS, mengembalikan pemetaan nama file -> digest heksadesimal.
func fetchSums(ctx context.Context, c *http.Client, rel *Release) (map[string]string, error) {
	asset, ok := rel.FindAsset(sumsAsset)
	if !ok {
		return nil, fmt.Errorf("Release ini tidak memiliki %s, tidak bisa memverifikasi integritas, upgrade ditolak", sumsAsset)
	}
	body, err := get(ctx, c, asset.URL)
	if err != nil {
		return nil, fmt.Errorf("mengunduh %s: %w", sumsAsset, err)
	}
	defer body.Close()

	raw, err := io.ReadAll(io.LimitReader(body, 1<<20))
	if err != nil {
		return nil, fmt.Errorf("membaca %s: %w", sumsAsset, err)
	}
	out := parseSums(string(raw))
	if len(out) == 0 {
		return nil, fmt.Errorf("isi %s kosong atau formatnya tidak dikenali", sumsAsset)
	}
	return out, nil
}

// parseSums mengurai daftar bergaya sha256sum, mengembalikan pemetaan nama file -> digest heksadesimal.
//
// Field pertama harus berupa heksadesimal 64 digit agar dimasukkan. Hanya memeriksa "tepat dua field" saja tidak cukup,
// karena baris keterangan dua kata apa pun bisa dianggap entri yang valid, memasukkan nilai sampah ke tabel digest,
// dan aset yang sebenarnya justru bisa cocok dengan digest yang salah.
func parseSums(raw string) map[string]string {
	out := map[string]string{}
	for line := range strings.Lines(raw) {
		// Formatnya "<sha256>  <filename>" (sha256sum memakai dua spasi; mode biner pada shasum menambahkan
		// prefiks * di nama file).
		fields := strings.Fields(strings.TrimSpace(line))
		if len(fields) != 2 || !isHexSHA256(fields[0]) {
			continue
		}
		name := strings.TrimPrefix(fields[1], "*")
		if name == "" {
			continue
		}
		out[name] = strings.ToLower(fields[0])
	}
	return out
}

func isHexSHA256(s string) bool {
	if len(s) != 64 {
		return false
	}
	for _, c := range s {
		switch {
		case c >= '0' && c <= '9', c >= 'a' && c <= 'f', c >= 'A' && c <= 'F':
		default:
			return false
		}
	}
	return true
}

// download menulis aset ke dst, sekaligus menghitung SHA256 dan melaporkan progres berdasarkan Content-Length.
func download(ctx context.Context, c *http.Client, a Asset, dst string, prog Progress) (string, error) {
	body, err := get(ctx, c, a.URL)
	if err != nil {
		return "", fmt.Errorf("mengunduh %s: %w", a.Name, err)
	}
	defer body.Close()

	f, err := os.Create(dst)
	if err != nil {
		return "", fmt.Errorf("membuat file sementara: %w", err)
	}
	defer f.Close()

	h := sha256.New()
	pw := &progressWriter{total: a.Size, prog: prog, name: a.Name, last: time.Now()}
	if _, err := io.Copy(io.MultiWriter(f, h, pw), body); err != nil {
		return "", fmt.Errorf("unduhan terputus: %w", err)
	}
	if err := f.Sync(); err != nil {
		return "", fmt.Errorf("gagal menulis ke disk: %w", err)
	}
	if a.Size > 0 && pw.written != a.Size {
		return "", fmt.Errorf("unduhan tidak lengkap: diharapkan %d byte, didapat %d byte", a.Size, pw.written)
	}
	return hex.EncodeToString(h.Sum(nil)), nil
}

// get mengirim satu permintaan GET yang dibatasi whitelist, mengembalikan body respons.
func get(ctx context.Context, c *http.Client, rawURL string) (io.ReadCloser, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, rawURL, nil)
	if err != nil {
		return nil, err
	}
	if err := checkURL(req.URL); err != nil {
		return nil, err
	}
	req.Header.Set("User-Agent", "artex-selfupdate")
	resp, err := c.Do(req)
	if err != nil {
		return nil, err
	}
	if resp.StatusCode != http.StatusOK {
		resp.Body.Close()
		return nil, fmt.Errorf("HTTP %d", resp.StatusCode)
	}
	return resp.Body, nil
}

// extractBinary mengambil file executable artex dari dalam paket rilis.
//
// Struktur di dalam paket adalah artex-<versi>-<os>-<arch>/artex, tapi di sini pencocokan dilakukan berdasarkan
// **nama dasar (base name)**, bukan menyusun path lengkap: nomor versi muncul sekali di nama paket, dan salah satu
// karakter saja pada penyusunan path akan membuat seluruh upgrade gagal, sehingga pencarian berdasarkan nama dasar lebih tahan terhadap perubahan.
func extractBinary(zipPath, dst string) error {
	want := "artex"
	if runtime.GOOS == "windows" {
		want = "artex.exe"
	}
	zr, err := zip.OpenReader(zipPath)
	if err != nil {
		return fmt.Errorf("membuka paket rilis: %w", err)
	}
	defer zr.Close()

	for _, entry := range zr.File {
		if entry.FileInfo().IsDir() || !strings.EqualFold(path.Base(entry.Name), want) {
			continue
		}
		rc, err := entry.Open()
		if err != nil {
			return fmt.Errorf("membaca %s: %w", entry.Name, err)
		}
		defer rc.Close()

		f, err := os.OpenFile(dst, os.O_CREATE|os.O_TRUNC|os.O_WRONLY, 0o755)
		if err != nil {
			return fmt.Errorf("menulis biner baru: %w", err)
		}
		defer f.Close()

		n, err := io.Copy(f, io.LimitReader(rc, maxBinarySize+1))
		if err != nil {
			return fmt.Errorf("mengekstrak %s: %w", entry.Name, err)
		}
		if n > maxBinarySize {
			return fmt.Errorf("file executable di dalam paket rilis melebihi %s, ekstraksi ditolak", humanSize(maxBinarySize))
		}
		if n == 0 {
			return fmt.Errorf("%s di dalam paket rilis adalah file kosong", want)
		}
		return f.Sync()
	}
	return fmt.Errorf("%s tidak ditemukan di dalam paket rilis", want)
}

// checkWritable memastikan lebih dulu bahwa direktori bisa ditulis. Tanpa langkah ini, saat berjalan bukan
// sebagai root, atau biner diletakkan di direktori sistem, kegagalan baru akan muncul saat proses penggantian
// biner, setelah mengunduh puluhan MB.
func checkWritable(dir string) error {
	probe, err := os.CreateTemp(dir, ".artex-update-probe-*")
	if err != nil {
		return fmt.Errorf("direktori program %s tidak dapat ditulis, tidak bisa melakukan update otomatis (periksa izin akses atau gunakan upgrade manual): %w", dir, err)
	}
	name := probe.Name()
	_ = probe.Close()
	_ = os.Remove(name)
	return nil
}

// progressWriter menghitung byte yang sudah ditulis dan membatasi frekuensi pelaporan, agar tidak mengirim satu event SSE untuk setiap chunk 32KiB.
type progressWriter struct {
	total   int64
	written int64
	name    string
	prog    Progress
	last    time.Time
}

func (w *progressWriter) Write(b []byte) (int, error) {
	w.written += int64(len(b))
	if time.Since(w.last) < 300*time.Millisecond {
		return len(b), nil
	}
	w.last = time.Now()
	pct := -1
	if w.total > 0 {
		pct = int(w.written * 100 / w.total)
	}
	w.prog(PhaseDownload, pct, fmt.Sprintf("Mengunduh %s / %s", humanSize(w.written), humanSize(w.total)))
	return len(b), nil
}

func humanSize(n int64) string {
	const unit = 1024
	if n < unit {
		return fmt.Sprintf("%d B", n)
	}
	div, exp := int64(unit), 0
	for v := n / unit; v >= unit; v /= unit {
		div *= unit
		exp++
	}
	return fmt.Sprintf("%.1f %cB", float64(n)/float64(div), "KMGT"[exp])
}

func short(sum string) string {
	if len(sum) > 12 {
		return sum[:12] + "…"
	}
	return sum
}
