package server

import (
	"time"

	"github.com/Autumn-27/artex/agent"
	"github.com/Autumn-27/artex/db"
)

// Parsing kebijakan retry sisi server, lihat docs/LLM重试设计.md. Dari lima layer:
//   - Connect / empty response / safety window per provider yang sama bersifat "mengikuti endpoint", tiap konfigurasi LLM boleh menimpa
//     default global (jika suatu field di profile dikosongkan, akan mewarisi default global; jika global juga tidak diset, memakai default bawaan);
//   - Circuit breaker / replay intent bersifat level-proses, hanya ada satu salinan global.
//
// Kebijakan global dibaca sekali dari satu baris settings di DB, titik pemanggilannya ada di jalur yang jarang dipakai (membangun provider, penutupan work,
// menyimpan konfigurasi), tidak sepadan untuk ditambah cache lagi; parameter circuit breaker adalah pengecualian, karena harus dibaca setiap kali di jalur kegagalan, sehingga
// didorong oleh applyRetryPolicy untuk disimpan ke Registry.

// retryPolicy reads the global policy; a nil DB yields the zero policy (all
// layers on their built-in defaults).
func (s *Server) retryPolicy() db.LLMRetryPolicy {
	if s.m == nil || s.m.pg == nil {
		return db.LLMRetryPolicy{}
	}
	return s.m.pg.LLMRetryPolicy()
}

// resolveRetry layers one profile's override on top of the global policy and
// converts the result into the form agent.Config carries. Rules combine field by
// field, so a profile that only pins an interval still inherits the global count.
func resolveRetry(o db.RetryOverride, pol db.LLMRetryPolicy) agent.RetryConfig {
	connect := o.Connect.Or(pol.Connect)
	empty := o.Empty.Or(pol.Empty)
	stream := o.Stream.Or(pol.Stream)
	return agent.RetryConfig{
		// Jumlah percobaan di sini tetap memakai semantik asli "0=default / negatif=nonaktif": MaxRetries milik SDK /
		// EmptyResponseRetries sepenuhnya isomorfik dengan ini, cukup diserahkan ke parsing-nya sendiri.
		ConnectAttempts: connect.Attempts, ConnectInterval: connect.Interval(),
		EmptyAttempts: empty.Attempts, EmptyInterval: empty.Interval(),
		StreamAttempts: stream.Attempts, StreamInterval: stream.Interval(),
	}
}

// applyProfileRetry fills cfg.Retry for a profile read from the DB.
func (s *Server) applyProfileRetry(cfg *agent.Config, p *db.LLMProfile) {
	if p == nil {
		return
	}
	cfg.Retry = resolveRetry(p.Retry, s.retryPolicy())
}

// Nilai default circuit breaker (cooldown polling) sama dengan default bawaan llmpool, di sini hanya ditimpa saat pengguna benar-benar mengatur nilainya.
// Nilai default replay intent ada di modelErrorRetries / modelErrorRetryBackoff pada engine.go.

// applyRetryPolicy pushes the process-wide layers of the policy into the objects
// that consume them on a hot path: the circuit-breaker registry. Called at
// startup and whenever the policy is saved.
func (s *Server) applyRetryPolicy() {
	pol := s.retryPolicy()
	if s.llmHealth != nil {
		s.llmHealth.SetPolicy(pol.Breaker.Attempts, pol.Breaker.Interval())
	}
}

// modelErrorRetryPolicy resolves the intent-level replay knobs (layer ⑤): how
// many times a model_error work is re-run and how long to back off between runs.
func (e *Engine) modelErrorRetryPolicy() (retries int, backoff time.Duration) {
	retries, backoff = modelErrorRetries, modelErrorRetryBackoff
	if e == nil || e.m == nil || e.m.pg == nil {
		return retries, backoff
	}
	rule := e.m.pg.LLMRetryPolicy().Intent
	if rule.Attempts != 0 {
		retries = max(rule.Attempts, 0)
	}
	if d := rule.Interval(); d > 0 {
		backoff = d
	}
	return retries, backoff
}

// emptyTurnNudgeLimit resolves how many empty-turn continuations one work may
// inject (lihat steerHooks.Stop). Ini sengaja memakai ulang kontrol milik layer kedua, "jumlah percobaan
// ulang respons kosong": keduanya adalah dua cara untuk hal yang sama. Layer SDK menangani "tidak ada satu pun content block", caranya dengan mengirim ulang
// request yang sama persis; di sini menangani "hanya ada thinking, tanpa isi maupun tool call", caranya dengan menambahkan satu instruksi agar
// model melanjutkan dari thinking yang sudah ada (mengirim ulang apa adanya tidak ada gunanya untuk kekosongan yang ditentukan oleh bentuk konteks ini). Kriteria penentuan kosong
// berbeda karena SDK berpatokan pada "apakah pernah yield event", sedangkan thinking increment itu sendiri adalah event, tapi saat pengguna mengatur
// "berapa kali retry respons kosong" yang dimaksud adalah "kalau model belum menghasilkan konten substantif, coba lagi", kedua layer memakai angka yang sama
// agar sesuai dengan maksud ini.
//
// Membaca kebijakan global, bukan override suatu profile: satu run di tengah jalan bisa berpindah profile karena failover, sedangkan ini
// adalah gerbang kuota total untuk keseluruhan intent, seharusnya tidak berubah hanya karena pindah endpoint. Semantiknya isomorfik dengan emptyRetries() milik SDK:
// 0 = default defaultEmptyTurnNudges; -1 (negatif) = menonaktifkan lanjutan kosong; >0 = memakai nilai tersebut.
func (e *Engine) emptyTurnNudgeLimit() int {
	if e == nil || e.m == nil || e.m.pg == nil {
		return defaultEmptyTurnNudges
	}
	switch n := e.m.pg.LLMRetryPolicy().Empty.Attempts; {
	case n == 0:
		return defaultEmptyTurnNudges
	case n < 0:
		return 0
	default:
		return n
	}
}
