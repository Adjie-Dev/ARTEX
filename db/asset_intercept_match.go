package db

import (
	"fmt"
	"net"
	"net/url"
	"strings"
)

// Lapisan pencocokan/eksekusi aturan intercept aset. asset_intercept.go hanya
// menangani penyimpanan aturan, di sini bertugas mencocokkan domain/IP/URL
// "aset target" dengan aturan yang aktif. Dipanggil oleh tool agent
// (add_intent, insert_assets) sebelum mengirim intent / menyisipkan aset,
// ditolak kalau cocok.

// AssetInterceptKindLabel mengembalikan label untuk kind, dipakai untuk pesan penjelasan ke agent.
func AssetInterceptKindLabel(kind string) string {
	switch kind {
	case "exact_domain":
		return "Domain (persis)"
	case "exact_ip":
		return "IP (persis)"
	case "exact_url":
		return "URL (persis)"
	case "fuzzy_domain":
		return "Domain (fuzzy)"
	case "fuzzy_ip":
		return "IP (fuzzy)"
	case "fuzzy_url":
		return "URL (fuzzy)"
	case "cidr":
		return "Blok CIDR"
	}
	return kind
}

// Reason mengembalikan alasan kecocokan yang mudah dibaca, dalam bentuk: cocok dengan aturan intercept aset [Domain (fuzzy): .gov.cn] (catatan).
func (r AssetInterceptRule) Reason() string {
	s := fmt.Sprintf("cocok dengan aturan intercept aset [%s: %s]", AssetInterceptKindLabel(r.Kind), r.Pattern)
	if note := strings.TrimSpace(r.Note); note != "" {
		s += " (" + note + ")"
	}
	return s
}

// matchOne menentukan apakah satu aturan aktif cocok dengan kandidat domain/IP/URL yang diberikan, mengembalikan nilai yang cocok.
func matchOne(r AssetInterceptRule, domains, ips, urls []string) (string, bool) {
	p := strings.TrimSpace(r.Pattern)
	if p == "" {
		return "", false
	}
	switch r.Kind {
	case "exact_domain":
		for _, d := range domains {
			if strings.EqualFold(strings.TrimSpace(d), p) {
				return d, true
			}
		}
	case "exact_ip":
		for _, ip := range ips {
			if strings.TrimSpace(ip) == p {
				return ip, true
			}
		}
	case "exact_url":
		for _, u := range urls {
			if strings.TrimSpace(u) == p {
				return u, true
			}
		}
	case "fuzzy_domain":
		lp := strings.ToLower(p)
		for _, d := range domains {
			if d != "" && strings.Contains(strings.ToLower(d), lp) {
				return d, true
			}
		}
	case "fuzzy_ip":
		for _, ip := range ips {
			if ip != "" && strings.Contains(ip, p) {
				return ip, true
			}
		}
	case "fuzzy_url":
		lp := strings.ToLower(p)
		for _, u := range urls {
			if u != "" && strings.Contains(strings.ToLower(u), lp) {
				return u, true
			}
		}
	case "cidr":
		_, ipnet, err := net.ParseCIDR(p)
		if err != nil {
			return "", false
		}
		for _, ip := range ips {
			if pip := net.ParseIP(strings.TrimSpace(ip)); pip != nil && ipnet.Contains(pip) {
				return ip, true
			}
		}
	}
	return "", false
}

// MatchAssetInterceptRules mengembalikan aturan aktif pertama yang cocok dengan
// kandidat domain/IP/URL yang diberikan, beserta nilai yang cocok tersebut.
// Dipakai oleh insert_assets untuk mencocokkan input mentah (assetInputItem yang belum disimpan ke database).
func MatchAssetInterceptRules(rules []AssetInterceptRule, domains, ips, urls []string) (AssetInterceptRule, string, bool) {
	for _, r := range rules {
		if !r.Enabled {
			continue
		}
		if v, ok := matchOne(r, domains, ips, urls); ok {
			return r, v, true
		}
	}
	return AssetInterceptRule{}, "", false
}

// interceptCandidates mengambil kandidat domain/IP/URL dari sebuah aset yang
// sudah tersimpan untuk pencocokan intercept. Host dari URL akan diekstrak dan
// dikategorikan, sehingga aset layanan yang "hanya punya URL" juga bisa cocok dengan aturan domain/IP.
func (a *Asset) interceptCandidates() (domains, ips, urls []string) {
	add := func(dst *[]string, s string) {
		if s = strings.TrimSpace(s); s != "" {
			*dst = append(*dst, s)
		}
	}
	add(&domains, a.Domain)
	add(&domains, a.RootDomain)
	for _, d := range a.BoundDomains {
		add(&domains, d)
	}
	add(&ips, a.IP)
	add(&urls, a.URL)
	if a.URL != "" {
		if u, err := url.Parse(a.URL); err == nil {
			if h := u.Hostname(); h != "" {
				if net.ParseIP(h) != nil {
					add(&ips, h)
				} else {
					add(&domains, h)
				}
			}
		}
	}
	return domains, ips, urls
}

// InterceptLabel mengembalikan identitas singkat sebuah aset, dipakai untuk pesan penjelasan ke agent.
func (a *Asset) InterceptLabel() string {
	var target string
	switch {
	case a.Domain != "":
		target = a.Domain
	case a.URL != "":
		target = a.URL
	case a.IP != "":
		target = a.IP
	default:
		target = fmt.Sprintf("#%d", a.ID)
	}
	return fmt.Sprintf("aset#%d[%s] %s", a.ID, a.Type, target)
}

// hasEnabledRule menentukan apakah ada aturan aktif di dalam kumpulan aturan.
func hasEnabledRule(rules []AssetInterceptRule) bool {
	for _, r := range rules {
		if r.Enabled {
			return true
		}
	}
	return false
}

// AssetGateDecision adalah hasil keputusan gerbang "intercept dulu, baru izinkan" untuk sekumpulan kandidat.
type AssetGateDecision struct {
	Allowed bool
	Reason  string // alasan ditolak (tanpa identitas aset); kosong kalau Allowed=true
}

// EvaluateAssetGate menjalankan keputusan gerbang tingkat tugas:
//  1. Cocok dengan salah satu blockRules yang aktif -> ditolak (dengan alasan intercept).
//  2. Kalau tidak, jika allowRules punya item aktif dan tidak ada yang cocok -> ditolak (tidak termasuk dalam cakupan yang diizinkan).
//  3. Selain itu -> diloloskan.
//
// Saat allowRules kosong/tidak ada item aktif, gerbang izin tidak berlaku
// (artinya whitelist tidak aktif, semua diloloskan), untuk menghindari semua
// aset terblokir hanya karena "aturan izin belum dikonfigurasi".
func EvaluateAssetGate(blockRules, allowRules []AssetInterceptRule, domains, ips, urls []string) AssetGateDecision {
	if rule, _, ok := MatchAssetInterceptRules(blockRules, domains, ips, urls); ok {
		return AssetGateDecision{Allowed: false, Reason: rule.Reason()}
	}
	if hasEnabledRule(allowRules) {
		if _, _, ok := MatchAssetInterceptRules(allowRules, domains, ips, urls); !ok {
			return AssetGateDecision{Allowed: false, Reason: "tidak termasuk dalam cakupan yang diizinkan (whitelist) untuk tugas ini, tidak boleh diuji"}
		}
	}
	return AssetGateDecision{Allowed: true}
}

// AssetInterceptHit menggambarkan sebuah aset yang ditolak oleh gerbang (cocok dengan intercept atau di luar cakupan yang diizinkan).
type AssetInterceptHit struct {
	Asset  *Asset
	Reason string // alasan yang mudah dibaca
}

// Describe mengembalikan penjelasan yang mudah dibaca: informasi aset + alasan.
func (h AssetInterceptHit) Describe() string {
	return fmt.Sprintf("%s → %s", h.Asset.InterceptLabel(), h.Reason)
}

// ListAssetInterceptRules adalah passthrough dari method dengan nama yang sama
// milik *DB, agar pemanggil yang hanya memegang AssetStore (seperti tool
// agent) juga bisa membaca aturan.
func (s *AssetStore) ListAssetInterceptRules() ([]AssetInterceptRule, error) {
	return s.db.ListAssetInterceptRules()
}

// CheckAssetsIntercept memuat aset berdasarkan id, menjalankan keputusan
// gerbang "intercept dulu, baru izinkan" satu per satu, mengembalikan semua
// aset yang ditolak. Aturan intercept = global gabung block tingkat tugas;
// aturan izin = allow tingkat tugas (hanya untuk tugas ini).
// Langsung kembali kalau tidak ada id. Memakai GetByIDs global (tidak difilter
// oleh scope tugas) untuk memastikan intercept tidak dilemahkan oleh scope.
func (s *AssetStore) CheckAssetsIntercept(taskID int64, ids []int64) ([]AssetInterceptHit, error) {
	if len(ids) == 0 {
		return nil, nil
	}
	blockRules, err := s.db.ListAssetInterceptRules()
	if err != nil {
		return nil, err
	}
	var allowRules []AssetInterceptRule
	if taskID > 0 {
		tb, ta, err := s.TaskInterceptRulesSplit(taskID)
		if err != nil {
			return nil, err
		}
		blockRules = append(blockRules, tb...)
		allowRules = ta
	}
	// Tidak ada aturan intercept maupun aturan izin yang aktif -> tidak perlu dinilai, semua diloloskan.
	if len(blockRules) == 0 && !hasEnabledRule(allowRules) {
		return nil, nil
	}
	assets, err := s.GetByIDs(ids)
	if err != nil {
		return nil, err
	}
	var hits []AssetInterceptHit
	for _, a := range assets {
		domains, ips, urls := a.interceptCandidates()
		if d := EvaluateAssetGate(blockRules, allowRules, domains, ips, urls); !d.Allowed {
			hits = append(hits, AssetInterceptHit{Asset: a, Reason: d.Reason})
		}
	}
	return hits, nil
}
