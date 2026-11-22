package selfupdate

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"strings"
	"time"
)

// Repo adalah sumber rilis. Ditulis permanen di kode, bukan dijadikan opsi
// konfigurasi, karena sumber update yang bisa dikonfigurasi sama saja dengan
// memberi siapa pun yang bisa mengubah konfigurasi satu jalur eksekusi kode
// jarak jauh, lubang ini tidak boleh dibuka untuk platform pentest.
const Repo = "Autumn-27/artex"

// latestURL adalah endpoint "versi resmi terbaru" GitHub. Endpoint ini otomatis
// melewati prerelease dan draft.
const latestURL = "https://api.github.com/repos/" + Repo + "/releases/latest"

// allowedHosts membatasi domain yang boleh diakses oleh jalur update. Bersama
// checkRedirect di bawah, setiap loncatan yang diarahkan ke host di luar
// daftar ini akan langsung gagal, ini adalah gerbang pertama untuk mencegah
// DNS poisoning / man-in-the-middle menukar binary, gerbang keduanya adalah
// pencocokan SHA256SUMS.
var allowedHosts = map[string]bool{
	"api.github.com":                       true,
	"github.com":                           true,
	"objects.githubusercontent.com":        true, // object storage tempat aset release sebenarnya disimpan
	"release-assets.githubusercontent.com": true,
	"raw.githubusercontent.com":            true,
}

// Release adalah field GitHub Release yang kita perhatikan.
type Release struct {
	TagName     string    `json:"tag_name"`
	Name        string    `json:"name"`
	Body        string    `json:"body"`
	Draft       bool      `json:"draft"`
	Prerelease  bool      `json:"prerelease"`
	PublishedAt time.Time `json:"published_at"`
	HTMLURL     string    `json:"html_url"`
	Assets      []Asset   `json:"assets"`
}

// Asset adalah satu file yang terlampir pada Release.
type Asset struct {
	Name string `json:"name"`
	URL  string `json:"browser_download_url"`
	Size int64  `json:"size"`
}

// NewClient membuat klien HTTP yang hanya mengenali domain GitHub. Jika
// proxy kosong, koneksi dilakukan langsung.
//
// Sengaja tidak memakai ulang Transport default: jalur update wajib memakai
// TLS dan memverifikasi sertifikat, tidak boleh terpengaruh oleh pengaturan
// seperti InsecureSkipVerify yang ditetapkan di tempat lain.
func NewClient(proxy string) *http.Client {
	tr := &http.Transport{
		ForceAttemptHTTP2:   true,
		TLSHandshakeTimeout: 15 * time.Second,
	}
	if p := strings.TrimSpace(proxy); p != "" {
		if pu, err := url.Parse(p); err == nil {
			tr.Proxy = http.ProxyURL(pu)
		}
	}
	return &http.Client{
		Transport: tr,
		Timeout:   30 * time.Minute, // mengunduh paket penuh, tidak boleh terhenti oleh timeout per-request
		CheckRedirect: func(req *http.Request, via []*http.Request) error {
			if len(via) >= 10 {
				return fmt.Errorf("terlalu banyak redirect")
			}
			return checkURL(req.URL)
		},
	}
}

// checkURL mewajibkan https + domain yang ada di whitelist.
func checkURL(u *url.URL) error {
	if u.Scheme != "https" {
		return fmt.Errorf("menolak alamat non-HTTPS: %s", u.Scheme+"://"+u.Host)
	}
	if !allowedHosts[strings.ToLower(u.Hostname())] {
		return fmt.Errorf("menolak domain non-GitHub: %s", u.Hostname())
	}
	return nil
}

// FetchLatest mengambil versi resmi terbaru.
func FetchLatest(ctx context.Context, c *http.Client) (*Release, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, latestURL, nil)
	if err != nil {
		return nil, err
	}
	if err := checkURL(req.URL); err != nil {
		return nil, err
	}
	req.Header.Set("Accept", "application/vnd.github+json")
	req.Header.Set("User-Agent", "artex-selfupdate")

	resp, err := c.Do(req)
	if err != nil {
		return nil, fmt.Errorf("gagal mengakses GitHub (proxy global bisa dikonfigurasi di pengaturan sistem): %w", err)
	}
	defer resp.Body.Close()

	switch {
	case resp.StatusCode == http.StatusForbidden, resp.StatusCode == http.StatusTooManyRequests:
		// API GitHub tanpa autentikasi dibatasi 60 kali per jam per IP, mudah tersandung saat berbagi IP keluar.
		return nil, fmt.Errorf("endpoint GitHub dibatasi rate-nya (60 kali per jam), coba lagi nanti")
	case resp.StatusCode == http.StatusNotFound:
		return nil, fmt.Errorf("repository %s belum pernah merilis versi resmi apa pun", Repo)
	case resp.StatusCode != http.StatusOK:
		return nil, fmt.Errorf("GitHub mengembalikan %d", resp.StatusCode)
	}

	var rel Release
	if err := json.NewDecoder(resp.Body).Decode(&rel); err != nil {
		return nil, fmt.Errorf("gagal mengurai Release: %w", err)
	}
	if strings.TrimSpace(rel.TagName) == "" {
		return nil, fmt.Errorf("Release tidak memiliki tag")
	}
	return &rel, nil
}

// AssetName mengembalikan nama paket rilis untuk platform saat ini, konsisten
// dengan package_binary di build.sh: artex-<versi>-<os>-<arch>.zip (nomor
// versi tanpa prefix v).
func AssetName(tag, goos, goarch string) string {
	return fmt.Sprintf("artex-%s-%s-%s.zip", strings.TrimPrefix(tag, "v"), goos, goarch)
}

// FindAsset mencari aset berdasarkan nama di dalam Release.
func (r *Release) FindAsset(name string) (Asset, bool) {
	for _, a := range r.Assets {
		if strings.EqualFold(a.Name, name) {
			return a, true
		}
	}
	return Asset{}, false
}
