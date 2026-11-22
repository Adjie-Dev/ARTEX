package server

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"net/http"
	"runtime"
	"sync"
	"time"

	"github.com/Autumn-27/artex/selfupdate"
)

// Permukaan HTTP untuk update sekali klik dari halaman web. Logika download,
// verifikasi, dan pergantian versi yang sebenarnya semuanya ada di package
// selfupdate, di sini hanya menangani batas otorisasi, mutex konkurensi,
// broadcast progres, dan memberi tahu main bahwa "sudah waktunya keluar".
//
// Restart tidak dilakukan oleh proses ini sendiri: setelah versi baru selesai
// di-staging, proses keluar dengan selfupdate.ExitRestart, lalu dijalankan
// ulang oleh script penjaga (start.sh / start.bat, atau ENTRYPOINT di Docker).

// restartCh ditutup setelah upgrade siap atau rollback selesai, main menerima sinyal ini lalu keluar dengan ExitRestart.
var (
	restartOnce sync.Once
	restartCh   = make(chan struct{})
)

// RestartRequested mengembalikan channel yang ditutup saat "harus keluar dan minta proses penjaga menjalankan ulang".
func RestartRequested() <-chan struct{} { return restartCh }

func requestRestart() { restartOnce.Do(func() { close(restartCh) }) }

// bootState adalah kesimpulan selfupdate.Bootstrap pada start kali ini
// (upgrade berhasil / baru di-rollback / file staging dibuang), disuntikkan
// oleh main, dipakai /api/update/check untuk memberi tahu frontend secara
// jujur hasil akhir upgrade sebelumnya.
var (
	bootStateMu sync.Mutex
	bootState   selfupdate.State
)

// SetBootUpdateState dipanggil sekali oleh main saat start.
func SetBootUpdateState(st selfupdate.State) {
	bootStateMu.Lock()
	defer bootStateMu.Unlock()
	bootState = st
}

func bootUpdateState() selfupdate.State {
	bootStateMu.Lock()
	defer bootStateMu.Unlock()
	return bootState
}

// releaseCache menyimpan cache hasil query versi terbaru dari GitHub.
//
// Notifikasi "ada versi baru" di top bar akan melakukan query setiap kali
// halaman penuh dimuat, sementara GitHub API tanpa autentikasi dibatasi 60
// kali per jam per IP, kalau tidak di-cache, membuka beberapa tab atau refresh
// halaman beberapa kali saja sudah menghabiskan kuota, sehingga saat benar-benar
// ingin update malah tidak bisa query lagi. Saat pengguna secara eksplisit
// mengklik "cek update", cache bisa dilewati dengan force.
type releaseCache struct {
	mu  sync.Mutex
	rel *selfupdate.Release
	err error
	at  time.Time
	// fetch adalah fungsi pengambil data, hanya titik injeksi untuk testing; kalau nil, pakai query GitHub yang sebenarnya.
	fetch func(context.Context, *http.Client) (*selfupdate.Release, error)
}

const (
	releaseTTL = 30 * time.Minute
	// Hasil gagal juga disimpan sebentar di cache, kalau tidak setiap kali halaman
	// dimuat akan menunggu timeout saat GitHub tidak terjangkau; tapi TTL-nya
	// dibuat pendek agar begitu jaringan pulih bisa langsung normal lagi.
	releaseErrTTL = 2 * time.Minute
	// Timeout untuk query. Timeout 30 menit di NewClient itu untuk download paket lengkap, query versi tidak boleh menunggu selama itu.
	releaseTimeout = 20 * time.Second
)

var relCache = &releaseCache{}

// get mengembalikan Release terbaru, kalau kena cache tidak perlu mengakses jaringan.
//
// Lock tetap dipegang selama pengambilan data: request yang berbarengan akan
// mengantre menunggu hasil query yang sama, bukan masing-masing memukul GitHub
// sendiri-sendiri (saat halaman baru dimuat dan beberapa tab query bersamaan,
// justru itu momen paling rawan memicu rate limit).
func (c *releaseCache) get(ctx context.Context, client *http.Client, force bool) (*selfupdate.Release, error) {
	c.mu.Lock()
	defer c.mu.Unlock()

	if !force {
		ttl := releaseTTL
		if c.err != nil {
			ttl = releaseErrTTL
		}
		if !c.at.IsZero() && time.Since(c.at) < ttl {
			return c.rel, c.err
		}
	}

	fetch := c.fetch
	if fetch == nil {
		fetch = selfupdate.FetchLatest
	}
	ctx, cancel := context.WithTimeout(ctx, releaseTimeout)
	defer cancel()
	rel, err := fetch(ctx, client)
	// Request yang dibatalkan (pengguna menutup tab) bukan berarti ada masalah di
	// GitHub, jangan dicatat ke cache, kalau tidak pengunjung berikutnya akan
	// mendapat error "dibatalkan" yang membingungkan.
	if err != nil && ctx.Err() != nil && errors.Is(ctx.Err(), context.Canceled) {
		return c.rel, err
	}
	c.rel, c.err, c.at = rel, err, time.Now()
	return rel, err
}

// updateProgress adalah satu paket progres yang dikirim ke frontend.
type updateProgress struct {
	Phase   selfupdate.Phase `json:"phase"`
	Percent int              `json:"percent"` // hanya berarti pada tahap download; selain itu -1
	Message string           `json:"message"`
	Version string           `json:"version,omitempty"`
	Error   string           `json:"error,omitempty"`
}

// updateHub memegang progres satu kali upgrade dan menyiarkannya ke subscriber SSE.
//
// running juga berfungsi sebagai mutex: POST /api/update/apply lagi saat upgrade
// masih berjalan langsung ditolak dengan 409, agar tidak ada dua goroutine yang
// menulis ke artex.new yang sama secara bersamaan.
type updateHub struct {
	mu      sync.Mutex
	running bool
	cur     updateProgress
	subs    map[chan updateProgress]struct{}
}

var updHub = &updateHub{
	cur:  updateProgress{Phase: selfupdate.PhaseIdle, Percent: -1},
	subs: map[chan updateProgress]struct{}{},
}

// begin merebut hak untuk melakukan upgrade, mengembalikan false kalau sudah ada yang berjalan.
func (h *updateHub) begin(version string) bool {
	h.mu.Lock()
	defer h.mu.Unlock()
	if h.running {
		return false
	}
	h.running = true
	h.cur = updateProgress{Phase: selfupdate.PhaseDownload, Percent: 0, Message: "Sedang menyiapkan...", Version: version}
	h.fanout(h.cur)
	return true
}

// finish mengakhiri satu kali upgrade. err bernilai nil berarti staging berhasil, menunggu restart.
func (h *updateHub) finish(err error) {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.running = false
	if err != nil {
		h.cur = updateProgress{Phase: selfupdate.PhaseFailed, Percent: -1, Message: "Update gagal", Error: err.Error(), Version: h.cur.Version}
	} else {
		h.cur = updateProgress{Phase: selfupdate.PhaseStaged, Percent: 100, Message: "Versi baru sudah siap, sedang restart...", Version: h.cur.Version}
	}
	h.fanout(h.cur)
}

func (h *updateHub) publish(ph selfupdate.Phase, pct int, msg string) {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.cur = updateProgress{Phase: ph, Percent: pct, Message: msg, Version: h.cur.Version}
	h.fanout(h.cur)
}

// fanout harus dipanggil saat h.mu sedang dipegang. Channel subscriber punya
// buffer, kalau penuh langsung dibuang, progres adalah informasi sesaat yang
// boleh hilang, koneksi SSE yang macet tidak boleh sampai menghambat upgrade itu sendiri.
func (h *updateHub) fanout(p updateProgress) {
	for ch := range h.subs {
		select {
		case ch <- p:
		default:
		}
	}
}

func (h *updateHub) snapshot() (updateProgress, bool) {
	h.mu.Lock()
	defer h.mu.Unlock()
	return h.cur, h.running
}

func (h *updateHub) subscribe() (<-chan updateProgress, func()) {
	ch := make(chan updateProgress, 64)
	h.mu.Lock()
	h.subs[ch] = struct{}{}
	h.mu.Unlock()
	var once sync.Once
	return ch, func() {
		once.Do(func() {
			h.mu.Lock()
			delete(h.subs, ch)
			h.mu.Unlock()
			close(ch)
		})
	}
}

// updateCheck melakukan query ke versi rilis resmi terbaru di GitHub dan membandingkannya dengan versi saat ini.
//
// Frontend juga bisa langsung terhubung ke api.github.com (CORS GitHub adalah
// *), tetapi endpoint inilah yang dijadikan acuan: download dilakukan oleh
// backend, jadi update hanya masuk akal kalau backend bisa mengakses GitHub.
// Kondisi browser bisa connect tapi server tidak bisa itu umum terjadi (server
// di jaringan internal, atau proxy hanya diset di browser), kalau begitu klik
// update pasti gagal, lebih baik dilaporkan secara jujur sejak langkah
// pengecekan ini.
func (s *Server) updateCheck(w http.ResponseWriter, r *http.Request) {
	current := BuildVersion
	mode := "binary"
	if selfupdate.InDocker() {
		mode = "docker"
	}
	boot := bootUpdateState()
	out := map[string]any{
		"current":     current,
		"mode":        mode,
		"os":          runtime.GOOS,
		"arch":        runtime.GOARCH,
		"has_backup":  selfupdate.HasBackup(),
		"repo":        selfupdate.Repo,
		"boot_notice": boot.Detail,
		"rolled_back": boot.RolledBack,
	}

	// Notifikasi di top bar memakai cache (default); saat pengguna klik "cek update" dikirim force=1 untuk memaksa query ulang ke sumbernya.
	force := r.URL.Query().Get("force") != ""
	client := selfupdate.NewClient(s.m.GlobalProxy())
	rel, err := relCache.get(r.Context(), client, force)
	if err != nil {
		out["error"] = err.Error()
		writeJSON(w, 200, out)
		return
	}

	latest := rel.TagName
	out["latest"] = latest
	out["notes"] = rel.Body
	out["html_url"] = rel.HTMLURL
	if !rel.PublishedAt.IsZero() {
		out["published_at"] = rel.PublishedAt.Format(time.RFC3339)
	}

	asset := selfupdate.AssetName(latest, runtime.GOOS, runtime.GOARCH)
	out["asset"] = asset
	if a, ok := rel.FindAsset(asset); ok {
		out["asset_available"] = true
		out["size"] = a.Size
	} else {
		out["asset_available"] = false
	}

	cmp, comparable := selfupdate.CompareVersions(current, latest)
	out["comparable"] = comparable
	out["has_update"] = comparable && cmp < 0
	if !comparable {
		// Build development (dev / git describe dengan suffix) tidak punya nomor
		// versi yang bisa dibandingkan. Kalau diloloskan, versi rilis resmi akan
		// menimpa binary debug lokal, jadi update langsung tidak diberikan.
		out["reason"] = fmt.Sprintf("Versi saat ini %q bukan versi rilis resmi, update sekali klik dinonaktifkan", current)
	}
	writeJSON(w, 200, out)
}

// updateApply mengunduh dan men-staging versi baru, setelah selesai proses keluar agar direstart oleh script penjaga.
//
// Langsung mengembalikan 202, pekerjaan sebenarnya berjalan di goroutine latar
// belakang: download paket lengkap bisa butuh beberapa menit, kalau digantungkan
// pada request akan terpotong oleh timeout reverse proxy. Progres dikirim lewat
// /api/update/stream.
func (s *Server) updateApply(w http.ResponseWriter, r *http.Request) {
	current := BuildVersion

	// Memakai cache: memastikan yang dipasang adalah versi yang sama dengan yang dilihat dan dikonfirmasi pengguna di tampilan.
	client := selfupdate.NewClient(s.m.GlobalProxy())
	rel, err := relCache.get(r.Context(), client, false)
	if err != nil {
		writeErr(w, 502, err.Error())
		return
	}
	cmp, comparable := selfupdate.CompareVersions(current, rel.TagName)
	if !comparable {
		writeErr(w, 400, fmt.Sprintf("Versi saat ini %q bukan versi rilis resmi, update sekali klik dinonaktifkan", current))
		return
	}
	if cmp >= 0 {
		writeErr(w, 400, fmt.Sprintf("Versi saat ini %s sudah yang terbaru", current))
		return
	}
	if !updHub.begin(rel.TagName) {
		writeErr(w, 409, "Sudah ada update yang sedang berjalan")
		return
	}

	go func() {
		// Sengaja memakai s.ctx, bukan ctx dari request: respons HTTP langsung
		// berakhir begitu dikembalikan, kalau download digantungkan padanya akan
		// langsung dibatalkan.
		err := selfupdate.Stage(s.ctx, client, rel, current, func(ph selfupdate.Phase, pct int, msg string) {
			updHub.publish(ph, pct, msg)
		})
		updHub.finish(err)
		if err != nil {
			log.Printf("[update] update gagal: %v", err)
			return
		}
		log.Printf("[update] %s -> %s sudah di-staging, akan keluar untuk menyelesaikan pergantian versi", current, rel.TagName)
		// Beri sedikit waktu untuk mengirim progres terakhir ke frontend, baru memicu keluar.
		time.Sleep(1500 * time.Millisecond)
		requestRestart()
	}()

	writeJSON(w, 202, map[string]any{"ok": true, "target": rel.TagName})
}

// updateRollback secara aktif mengembalikan ke versi sebelumnya (artex.old yang dibackup sebelum pergantian versi).
func (s *Server) updateRollback(w http.ResponseWriter, r *http.Request) {
	if _, running := updHub.snapshot(); running {
		writeErr(w, 409, "Update sedang berjalan, tidak bisa rollback")
		return
	}
	if err := selfupdate.Rollback(); err != nil {
		writeErr(w, 400, err.Error())
		return
	}
	log.Printf("[update] sudah rollback manual ke versi sebelumnya, akan keluar untuk menyelesaikan pergantian")
	writeJSON(w, 202, map[string]any{"ok": true})
	go func() {
		time.Sleep(500 * time.Millisecond)
		requestRestart()
	}()
}

// updateStream mengirim progres update lewat SSE.
func (s *Server) updateStream(w http.ResponseWriter, r *http.Request) {
	flusher, ok := w.(http.Flusher)
	if !ok {
		writeErr(w, 500, "streaming unsupported")
		return
	}
	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("Connection", "keep-alive")
	w.Header().Set("X-Accel-Buffering", "no")

	ch, unsub := updHub.subscribe()
	defer unsub()

	send := func(p updateProgress) {
		b, _ := json.Marshal(p)
		fmt.Fprintf(w, "data: %s\n\n", b)
		flusher.Flush()
	}
	// Kirim dulu satu state saat ini, agar setelah halaman di-refresh bisa langsung terlihat upgrade yang sedang berjalan.
	cur, _ := updHub.snapshot()
	send(cur)

	ctx := r.Context()
	ping := time.NewTicker(20 * time.Second)
	defer ping.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case p, ok := <-ch:
			if !ok {
				return
			}
			send(p)
		case <-ping.C:
			fmt.Fprint(w, ": ping\n\n")
			flusher.Flush()
		}
	}
}
