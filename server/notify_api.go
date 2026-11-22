package server

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/Autumn-27/artex/db"
	"github.com/Autumn-27/artex/notify"
)

// File ini adalah antarmuka HTTP untuk fitur notifikasi. Semua route dipasang
// setelah requireAuth (lihat Handler()), konsisten dengan endpoint admin lainnya.

// notifyChannelDTO adalah representasi channel yang diekspos ke luar.
//
// Config adalah konfigurasi **yang sudah dimasking**: field kredensial diganti
// dengan nilai berawalan notify.MaskedPrefix. Jika frontend mengirim balik nilai
// yang dimasking apa adanya, itu berarti "field ini tidak diubah", dan server
// akan mempertahankan nilai asli di database (lihat notify.MergeConfig).
type notifyChannelDTO struct {
	ID         int64          `json:"id"`
	Name       string         `json:"name"`
	Kind       string         `json:"kind"`
	Enabled    bool           `json:"enabled"`
	Mode       string         `json:"mode"`
	Config     map[string]any `json:"config"`
	Filter     notify.Filter  `json:"filter"`
	RatePerMin int            `json:"rate_per_min"`
	CreatedAt  time.Time      `json:"created_at"`
	UpdatedAt  time.Time      `json:"updated_at"`
	// SecretKeys memberi tahu frontend field mana yang merupakan kredensial,
	// sehingga bisa dirender sebagai kotak password dengan petunjuk "kosongkan
	// agar tidak diubah". Dideklarasikan oleh channel itu sendiri
	// (notify.Channel.SecretKeys), frontend tidak melakukan hardcode pengetahuan channel.
	SecretKeys []string `json:"secret_keys"`
}

// notifyDeliveryDTO adalah representasi riwayat pengiriman yang diekspos ke luar.
type notifyDeliveryDTO struct {
	ID          int64      `json:"id"`
	FindingID   int64      `json:"finding_id,string"`
	EventKind   string     `json:"event_kind"`
	ChannelID   int64      `json:"channel_id"`
	ChannelName string     `json:"channel_name"`
	ChannelKind string     `json:"channel_kind"`
	State       string     `json:"state"`
	Attempts    int        `json:"attempts"`
	LastError   string     `json:"last_error"`
	BatchID     *int64     `json:"batch_id,omitempty"`
	CreatedAt   time.Time  `json:"created_at"`
	SentAt      *time.Time `json:"sent_at,omitempty"`
	NextAttempt time.Time  `json:"next_attempt_at"`
	// Ringkasan judul pesan, agar daftar riwayat tidak perlu dibuka untuk tahu
	// isi notifikasi ini.
	Title    string `json:"title"`
	Severity string `json:"severity"`
}

func toNotifyChannelDTO(ch *db.NotificationChannel) notifyChannelDTO {
	var cfg map[string]any
	if len(ch.Config) > 0 {
		_ = json.Unmarshal(ch.Config, &cfg)
	}
	if cfg == nil {
		cfg = map[string]any{}
	}
	secrets := []string{}
	if c, ok := notify.Get(ch.Kind); ok {
		secrets = c.SecretKeys()
	}
	return notifyChannelDTO{
		ID:         ch.ID,
		Name:       ch.Name,
		Kind:       ch.Kind,
		Enabled:    ch.IsEnabled(),
		Mode:       ch.Mode,
		Config:     notify.MaskConfig(ch.Kind, cfg),
		Filter:     notify.ParseFilter(ch.Filter),
		RatePerMin: ch.RatePerMin,
		CreatedAt:  ch.CreatedAt,
		UpdatedAt:  ch.UpdatedAt,
		SecretKeys: secrets,
	}
}

func toNotifyDeliveryDTO(dl *db.NotificationDelivery) notifyDeliveryDTO {
	snap, _ := parseSnapshot(dl)
	dto := notifyDeliveryDTO{
		ID:          dl.ID,
		FindingID:   dl.FindingID,
		EventKind:   dl.EventKind,
		ChannelID:   dl.ChannelID,
		ChannelName: dl.ChannelName,
		ChannelKind: dl.ChannelKind,
		State:       dl.State,
		Attempts:    dl.Attempts,
		LastError:   dl.LastError,
		BatchID:     dl.BatchID,
		CreatedAt:   dl.CreatedAt,
		SentAt:      dl.SentAt,
		NextAttempt: dl.NextAttemptAt,
		Severity:    snap.Severity,
	}
	if snap.Name != "" {
		dto.Title = snap.Name
	} else {
		dto.Title = snap.VulnClass
	}
	return dto
}

// notifyMeta mengembalikan metadata statis dan setelan global yang dibutuhkan
// halaman notifikasi dalam satu kali permintaan, agar frontend tidak perlu
// mengirim tiga permintaan hanya untuk merender satu dropdown.
func (s *Server) notifyMeta(w http.ResponseWriter, r *http.Request) {
	pg := s.pg(w)
	if pg == nil {
		return
	}
	kinds := make([]map[string]any, 0, len(notify.Kinds()))
	for _, k := range notify.Kinds() {
		ch, _ := notify.Get(k)
		kinds = append(kinds, map[string]any{
			"kind":                 k,
			"default_rate_per_min": ch.DefaultRatePerMin(),
			"secret_keys":          ch.SecretKeys(),
		})
	}
	baseURL, _, _ := pg.GetSetting(settingNotifyPublicBaseURL)
	digest, _, _ := pg.GetSetting(settingNotifyDigestMinutes)
	stats, err := pg.NotificationStatsSnapshot(r.Context())
	if err != nil {
		writeErr(w, 500, err.Error())
		return
	}
	writeJSON(w, 200, map[string]any{
		"kinds":               kinds,
		"enabled":             pg.GetBool(settingNotifyEnabled, true),
		"public_base_url":     baseURL,
		"digest_interval_min": digest,
		"defaults": map[string]any{
			"digest_interval_min": notifyDefaultDigestMinutes,
		},
		"stats": stats,
	})
}

func (s *Server) notifyListChannels(w http.ResponseWriter, r *http.Request) {
	pg := s.pg(w)
	if pg == nil {
		return
	}
	channels, err := pg.ListNotificationChannels(r.Context())
	if err != nil {
		writeErr(w, 500, err.Error())
		return
	}
	out := make([]notifyChannelDTO, 0, len(channels))
	for _, ch := range channels {
		out = append(out, toNotifyChannelDTO(ch))
	}
	writeJSON(w, 200, map[string]any{"channels": out})
}

// notifyChannelRequest adalah body permintaan untuk membuat/memperbarui channel.
//
// Semua field bisnis memakai pointer, agar bisa membedakan "tidak dikirim"
// dari "dikirim dengan nilai zero": dalam semantik PATCH, field yang tidak
// dikirim harus mempertahankan nilai asli di database.
type notifyChannelRequest struct {
	Name       *string        `json:"name"`
	Kind       *string        `json:"kind"`
	Enabled    *bool          `json:"enabled"`
	Mode       *string        `json:"mode"`
	Config     map[string]any `json:"config"`
	Filter     *notify.Filter `json:"filter"`
	RatePerMin *int           `json:"rate_per_min"`
}

func (s *Server) notifyCreateChannel(w http.ResponseWriter, r *http.Request) {
	pg := s.pg(w)
	if pg == nil {
		return
	}
	var req notifyChannelRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeErr(w, 400, "body permintaan bukan JSON yang valid: "+err.Error())
		return
	}
	if req.Kind == nil || !notify.ValidKind(*req.Kind) {
		writeErr(w, 400, fmt.Sprintf("jenis channel tidak valid, pilihan yang tersedia: %s", strings.Join(notify.Kinds(), " / ")))
		return
	}
	name := ""
	if req.Name != nil {
		name = strings.TrimSpace(*req.Name)
	}
	if name == "" {
		writeErr(w, 400, "nama channel belum diisi")
		return
	}
	channel, _ := notify.Get(*req.Kind)
	if err := channel.Validate(req.Config); err != nil {
		writeErr(w, 400, err.Error())
		return
	}
	ch := &db.NotificationChannel{
		Name:       name,
		Kind:       *req.Kind,
		Enabled:    req.Enabled,
		Mode:       db.NotifyModeRealtime,
		RatePerMin: channel.DefaultRatePerMin(),
	}
	if req.Mode != nil {
		if !db.ValidNotifyMode(*req.Mode) {
			writeErr(w, 400, "mode notifikasi tidak valid, pilihan yang tersedia: realtime / digest")
			return
		}
		ch.Mode = *req.Mode
	}
	if req.RatePerMin != nil {
		// Nilai yang dikirim secara eksplisit langsung dipakai, termasuk 0, yang
		// berarti "tidak dibatasi laju" dan merupakan konfigurasi yang sah.
		if *req.RatePerMin < 0 {
			writeErr(w, 400, "nilai rate limit tidak boleh negatif")
			return
		}
		ch.RatePerMin = *req.RatePerMin
	}
	// Nilai default channel hanya dipakai saat field benar-benar tidak dikirim.
	// Keputusan ini harus diambil di sini, bukan di lapisan db: hanya body
	// permintaan yang bisa membedakan "field tidak dikirim" dari "dikirim
	// dengan nilai 0 secara eksplisit", dan kedua hal itu artinya sama sekali
	// berbeda (yang pertama = pakai default, yang kedua = tidak dibatasi laju).
	// Jika lapisan db menganggap 0 sebagai "tidak ditentukan", konfigurasi
	// tanpa rate limit jadi tidak bisa dicapai.
	if req.RatePerMin == nil {
		ch.RatePerMin = channel.DefaultRatePerMin()
	}
	if req.Filter != nil {
		// Validasi field filter yang nilainya terbatas (misalnya min_severity) saat
		// disimpan. Lihat notify.Filter.Validate: salah ketik pada ambang batas akan
		// membuat filter diam-diam gagal dan meneruskan semua notifikasi, jadi ini
		// harus dicegah di titik masuk.
		if err := req.Filter.Validate(); err != nil {
			writeErr(w, 400, err.Error())
			return
		}
		raw, _ := json.Marshal(req.Filter)
		ch.Filter = raw
	}
	rawCfg, _ := json.Marshal(req.Config)
	ch.Config = rawCfg

	id, err := pg.SaveNotificationChannel(r.Context(), ch)
	if err != nil {
		writeErr(w, 500, err.Error())
		return
	}
	writeJSON(w, 200, map[string]any{"id": id})
}

func (s *Server) notifyUpdateChannel(w http.ResponseWriter, r *http.Request) {
	pg := s.pg(w)
	if pg == nil {
		return
	}
	id, ok := pathInt(r, "id")
	if !ok {
		writeErr(w, 400, "id channel tidak valid")
		return
	}
	current, err := pg.NotificationChannelByID(r.Context(), id)
	if err != nil {
		notifyChannelLookupErr(w, err)
		return
	}
	var req notifyChannelRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeErr(w, 400, "body permintaan bukan JSON yang valid: "+err.Error())
		return
	}

	// kind boleh diubah, tapi mengubah tipe berarti seluruh field kredensial
	// diganti total dan tidak bisa digabung dengan konfigurasi lama.
	kind := current.Kind
	if req.Kind != nil {
		if !notify.ValidKind(*req.Kind) {
			writeErr(w, 400, fmt.Sprintf("jenis channel tidak valid, pilihan yang tersedia: %s", strings.Join(notify.Kinds(), " / ")))
			return
		}
		kind = *req.Kind
	}
	channel, _ := notify.Get(kind)

	var stored map[string]any
	if kind == current.Kind {
		if len(current.Config) > 0 {
			_ = json.Unmarshal(current.Config, &stored)
		}
	}
	if stored == nil {
		stored = map[string]any{}
	}
	// Memakai PrepareConfigUpdate, bukan MergeConfig polos: saat alamat tujuan
	// berubah, operator harus menyatakan ulang field kredensial secara eksplisit,
	// kalau tidak "hanya ubah alamat, kredensial tetap lama" akan mengirim
	// kredensial asli di database ke alamat yang baru.
	merged, err := notify.PrepareConfigUpdate(kind, stored, req.Config)
	if err != nil {
		writeErr(w, 400, err.Error())
		return
	}
	if err := channel.Validate(merged); err != nil {
		writeErr(w, 400, err.Error())
		return
	}
	rawCfg, _ := json.Marshal(merged)

	ch := &db.NotificationChannel{
		ID:         id,
		Name:       current.Name,
		Kind:       kind,
		Enabled:    current.Enabled,
		Mode:       current.Mode,
		Config:     rawCfg,
		Filter:     current.Filter,
		RatePerMin: current.RatePerMin,
	}
	if req.Name != nil {
		if ch.Name = strings.TrimSpace(*req.Name); ch.Name == "" {
			writeErr(w, 400, "nama channel tidak boleh kosong")
			return
		}
	}
	if req.Enabled != nil {
		ch.Enabled = req.Enabled
	}
	if req.Mode != nil {
		if !db.ValidNotifyMode(*req.Mode) {
			writeErr(w, 400, "mode notifikasi tidak valid, pilihan yang tersedia: realtime / digest")
			return
		}
		ch.Mode = *req.Mode
	}
	if req.RatePerMin != nil {
		if *req.RatePerMin < 0 {
			writeErr(w, 400, "nilai rate limit tidak boleh negatif")
			return
		}
		ch.RatePerMin = *req.RatePerMin
	}
	if req.Filter != nil {
		if err := req.Filter.Validate(); err != nil {
			writeErr(w, 400, err.Error())
			return
		}
		raw, _ := json.Marshal(req.Filter)
		ch.Filter = raw
	}

	// Memakai jalur SetNotificationChannelEnabled, bukan SaveNotificationChannel,
	// agar saat "dimatikan" semua pengiriman yang masih menunggu ikut ditandai
	// skipped, supaya saat diaktifkan kembali tidak langsung menerima tumpukan
	// pesan basi yang menumpuk.
	enabledChanged := ch.Enabled != nil && current.Enabled != nil && *ch.Enabled != *current.Enabled
	if enabledChanged {
		// Simpan dulu update konfigurasi ke database (saat ini enabled masih
		// memakai nilai lama, agar tidak memicu logika skip lebih awal), baru
		// kemudian mengganti saklarnya secara terpisah. Tidak ada jendela
		// konkurensi di antara dua langkah ini: endpoint ini adalah satu-satunya
		// pintu masuk yang mengubah kedua field tersebut.
		prev := ch.Enabled
		ch.Enabled = current.Enabled
		if _, err := pg.SaveNotificationChannel(r.Context(), ch); err != nil {
			writeErr(w, 500, err.Error())
			return
		}
		if err := pg.SetNotificationChannelEnabled(r.Context(), id, *prev); err != nil {
			writeErr(w, 500, err.Error())
			return
		}
		writeJSON(w, 200, map[string]any{"id": id})
		return
	}
	if _, err := pg.SaveNotificationChannel(r.Context(), ch); err != nil {
		writeErr(w, 500, err.Error())
		return
	}
	writeJSON(w, 200, map[string]any{"id": id})
}

func (s *Server) notifyDeleteChannel(w http.ResponseWriter, r *http.Request) {
	pg := s.pg(w)
	if pg == nil {
		return
	}
	id, ok := pathInt(r, "id")
	if !ok {
		writeErr(w, 400, "id channel tidak valid")
		return
	}
	if err := pg.DeleteNotificationChannel(r.Context(), id); err != nil {
		notifyChannelLookupErr(w, err)
		return
	}
	writeJSON(w, 200, map[string]any{"ok": true})
}

// notifyTestChannel mengirim satu pesan uji memakai konfigurasi yang tersimpan saat ini.
//
// Memanggil Send milik channel secara langsung, tanpa lewat antrean pengiriman:
// tujuan tes ini adalah memberi tahu pengguna seketika apakah "konfigurasi ini
// bisa terkirim atau tidak". Jika lewat antrean, hasilnya akan tersembunyi di
// riwayat pengiriman dan pengguna harus membukanya lagi untuk tahu berhasil
// atau tidak. Karena itu endpoint ini bersifat **sinkron**, dengan batas waktu
// ditentukan oleh HTTP client milik package notify (15 detik).
func (s *Server) notifyTestChannel(w http.ResponseWriter, r *http.Request) {
	pg := s.pg(w)
	if pg == nil {
		return
	}
	id, ok := pathInt(r, "id")
	if !ok {
		writeErr(w, 400, "id channel tidak valid")
		return
	}
	ch, err := pg.NotificationChannelByID(r.Context(), id)
	if err != nil {
		notifyChannelLookupErr(w, err)
		return
	}
	channel, ok := notify.Get(ch.Kind)
	if !ok {
		writeErr(w, 400, fmt.Sprintf("jenis channel %q tidak terdaftar", ch.Kind))
		return
	}
	var cfg map[string]any
	if len(ch.Config) > 0 {
		_ = json.Unmarshal(ch.Config, &cfg)
	}
	if err := channel.Validate(cfg); err != nil {
		writeErr(w, 400, err.Error())
		return
	}
	msg := notifyTestMessage(s.notifierBaseURL(pg))
	start := time.Now()
	// Pesan uji hanya satu, jumlah yang terkirim tidak perlu dihitung di sini
	// (batas panjang channel untuk satu pesan sudah ditangani oleh pemotongan,
	// tidak melibatkan pembagian segmen).
	if _, err := channel.Send(r.Context(), cfg, msg); err != nil {
		// Teruskan error asli dari channel apa adanya ke pengguna, karena itu
		// adalah satu-satunya petunjuk mereka untuk men-debug konfigurasinya.
		writeErr(w, 502, err.Error())
		return
	}
	writeJSON(w, 200, map[string]any{
		"ok":         true,
		"latency_ms": time.Since(start).Milliseconds(),
	})
}

// notifyTestMessage menyusun pesan uji. Isinya dibuat sengaja agar sekilas
// terlihat sebagai tes, supaya penerima tidak salah mengira ini temuan nyata.
func notifyTestMessage(baseURL string) notify.Message {
	return notify.Message{
		Items: []notify.Item{{
			FindingID: 0,
			Name:      "Pesan uji · konfigurasi channel normal",
			VulnClass: "Tes konektivitas",
			Severity:  "low",
			Summary:   "Ini adalah pesan uji dari channel notifikasi ARTEX. Jika pesan ini diterima, berarti konfigurasi channel ini berfungsi.",
			Assets:    []string{"artex.example.com"},
			DetailURL: baseURL,
		}},
		HomeURL: baseURL,
	}
}

// notifierBaseURL membaca alamat eksternal yang dipakai untuk tautan balik.
func (s *Server) notifierBaseURL(pg *db.DB) string {
	v, _, _ := pg.GetSetting(settingNotifyPublicBaseURL)
	return trimTrailingSlash(v)
}

func (s *Server) notifyListDeliveries(w http.ResponseWriter, r *http.Request) {
	pg := s.pg(w)
	if pg == nil {
		return
	}
	f := db.NotificationDeliveryFilter{
		State:     r.URL.Query().Get("state"),
		EventKind: r.URL.Query().Get("event_kind"),
	}
	if v := r.URL.Query().Get("channel_id"); v != "" {
		f.ChannelID = int64(atoiDefault(v, 0))
	}
	page := queryInt(r, "page", 1)
	pageSize := queryInt(r, "page_size", 50)
	items, total, err := pg.ListNotificationDeliveries(r.Context(), f, page, pageSize)
	if err != nil {
		writeErr(w, 500, err.Error())
		return
	}
	out := make([]notifyDeliveryDTO, 0, len(items))
	for _, dl := range items {
		out = append(out, toNotifyDeliveryDTO(dl))
	}
	writeJSON(w, 200, map[string]any{"deliveries": out, "total": total, "page": page, "page_size": pageSize})
}

func (s *Server) notifyRetryDelivery(w http.ResponseWriter, r *http.Request) {
	pg := s.pg(w)
	if pg == nil {
		return
	}
	id, ok := pathInt(r, "id")
	if !ok {
		writeErr(w, 400, "id pengiriman tidak valid")
		return
	}
	if err := pg.RetryNotificationDelivery(r.Context(), id); err != nil {
		writeErr(w, 400, err.Error())
		return
	}
	writeJSON(w, 200, map[string]any{"ok": true})
}

// notifyChannelLookupErr menerjemahkan "channel tidak ada" menjadi 404, error lainnya menjadi 500.
func notifyChannelLookupErr(w http.ResponseWriter, err error) {
	if errors.Is(err, db.ErrNotificationChannelNotFound) {
		writeErr(w, 404, "channel notifikasi tidak ditemukan")
		return
	}
	writeErr(w, 500, err.Error())
}
