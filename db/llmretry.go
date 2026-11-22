package db

import (
	"encoding/json"
	"time"
)

// Strategi retry LLM: konfigurasi global "jumlah + interval" untuk lima lapis
// retry, lihat docs/desain-retry-llm.md. Disimpan sebagai satu nilai JSON di
// tabel settings, karena ini parameter runtime yang hanya satu per instance,
// tidak layak dibuatkan tabel sendiri; pembacaannya jatuh kembali ke default
// bawaan, jadi saat key belum ada (database baru/belum pernah dikonfigurasi)
// perilakunya sama persis seperti saat masih berupa konstanta yang ditulis langsung di kode.

const settingLLMRetryPolicy = "llm_retry_policy"

// RetryRule is one layer's knob pair. The zero value means "unset":
//
//	Attempts   0 = pakai jumlah default bawaan; -1 = matikan retry lapisan ini; >0 = pakai nilai ini
//	IntervalMS 0 = pakai strategi interval aslinya (biasanya exponential backoff); >0 = pakai interval tetap dalam milidetik
//
// -1 berarti "dimatikan secara eksplisit", bukan "0 kali", karena nilai 0 sudah dipakai untuk arti "belum dikonfigurasi".
type RetryRule struct {
	Attempts   int `json:"attempts"`
	IntervalMS int `json:"interval_ms"`
}

// Interval returns the configured fixed interval, or 0 when unset (caller keeps
// its own default ladder).
func (r RetryRule) Interval() time.Duration {
	if r.IntervalMS <= 0 {
		return 0
	}
	return time.Duration(r.IntervalMS) * time.Millisecond
}

// Or returns the rule with each unset field filled in from fallback. Used to
// layer a profile override on top of the global policy field by field, so a
// profile that only pins the interval still inherits the global count.
func (r RetryRule) Or(fallback RetryRule) RetryRule {
	if r.Attempts == 0 {
		r.Attempts = fallback.Attempts
	}
	if r.IntervalMS == 0 {
		r.IntervalMS = fallback.IntervalMS
	}
	return r
}

// retry knob bounds. A count above the cap turns a blip into a token bonfire;
// an interval above an hour outlives any transient failure worth waiting out.
const (
	maxRetryAttempts   = 20
	maxRetryIntervalMS = 3600_000 // 1h
)

// Clamped returns the rule with out-of-range values pulled back into the sane
// band (attempts within [-1, 20], interval within [0, 1h]).
func (r RetryRule) Clamped() RetryRule {
	if r.Attempts < -1 {
		r.Attempts = -1
	}
	if r.Attempts > maxRetryAttempts {
		r.Attempts = maxRetryAttempts
	}
	if r.IntervalMS < 0 {
		r.IntervalMS = 0
	}
	if r.IntervalMS > maxRetryIntervalMS {
		r.IntervalMS = maxRetryIntervalMS
	}
	return r
}

// Clamped bounds a profile's override the same way the global policy is bounded,
// so a hand-crafted API payload can't land a value the CHECK constraint rejects.
func (o RetryOverride) Clamped() RetryOverride {
	o.Connect, o.Empty, o.Stream = o.Connect.Clamped(), o.Empty.Clamped(), o.Stream.Clamped()
	return o
}

// LLMRetryPolicy holds the five-layer retry configuration. Connect/Empty/Stream are the
// per-request layers (a profile may override them, see LLMProfile.Retry);
// Breaker and Intent are process-wide by nature and live only here.
type LLMRetryPolicy struct {
	// Connect: retry pembentukan koneksi SDK (connection reset/timeout/429/5xx, sebelum stream mulai). Default 3 kali, exponential backoff.
	Connect RetryRule `json:"connect"`
	// Empty: retry respons kosong dari SDK (selesai tapi tanpa content block, hanya format openai). Default 2 kali, exponential backoff.
	Empty RetryRule `json:"empty"`
	// Stream: retry jendela aman provider yang sama (replay karena putus stream sebelum output terkirim). Default 2 kali, mulai 0.5s exponential (dibatasi maksimal 4s).
	Stream RetryRule `json:"stream"`
	// Breaker: circuit breaker untuk pool. Attempts = berapa kali kegagalan sesaat
	// berturut-turut memicu circuit breaker (default 3, -1 = kegagalan sesaat
	// tidak memicu breaker, sedangkan kegagalan keras seperti saldo habis/key
	// tidak valid tetap langsung memicu); IntervalMS = durasi cooldown tetap (0 = memakai tangga default 1/5/30 menit).
	Breaker RetryRule `json:"breaker"`
	// Intent: worker menjalankan ulang seluruh intent setelah berakhir dengan model_error. Default 2 kali, tetap 3 detik.
	Intent RetryRule `json:"intent"`
}

// Clamped returns the policy with every rule clamped.
func (p LLMRetryPolicy) Clamped() LLMRetryPolicy {
	p.Connect, p.Empty, p.Stream = p.Connect.Clamped(), p.Empty.Clamped(), p.Stream.Clamped()
	p.Breaker, p.Intent = p.Breaker.Clamped(), p.Intent.Clamped()
	return p
}

// LLMRetryPolicy reads the global retry policy. A missing or unparseable value
// yields the zero policy — i.e. every layer on its built-in default.
func (d *DB) LLMRetryPolicy() LLMRetryPolicy {
	var p LLMRetryPolicy
	if d == nil {
		return p
	}
	raw, ok, err := d.GetSetting(settingLLMRetryPolicy)
	if err != nil || !ok || raw == "" {
		return p
	}
	if err := json.Unmarshal([]byte(raw), &p); err != nil {
		return LLMRetryPolicy{}
	}
	return p.Clamped()
}

// SetLLMRetryPolicy persists the global retry policy (values are clamped first).
func (d *DB) SetLLMRetryPolicy(p LLMRetryPolicy) error {
	raw, err := json.Marshal(p.Clamped())
	if err != nil {
		return err
	}
	return d.SetSetting(settingLLMRetryPolicy, string(raw))
}
