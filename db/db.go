// Package db is the PostgreSQL data source for ARTEX (menggantikan graph SQLite file tunggal yang lama).
// Package ini membuka koneksi, menerapkan schema, dan melakukan seed agent bawaan beserta katalog variabelnya.
package db

import (
	"context"
	"database/sql"
	_ "embed"
	"errors"
	"fmt"
	"net/url"
	"strings"
	"time"

	"github.com/Autumn-27/artex/config"
	"github.com/jackc/pgx/v5/pgconn"
	_ "github.com/jackc/pgx/v5/stdlib" // pgx database/sql driver ("pgx")
)

//go:embed schema.sql
var schemaSQL string

const schemaMigrationLockKey int64 = 7337741001

// maxOpenConns adalah batas atas connection pool, lihat penjelasan di Open. Nilainya jauh di bawah default PostgreSQL
// max_connections=100, sekaligus jauh di atas kedalaman pengambilan koneksi bertingkat milik aplikasi sendiri (advisory lock schema saat startup
// tetap memegang satu koneksi sementara seedBuiltins mengambil koneksi lain), sehingga tidak terjadi self-lock.
const maxOpenConns = 32

var schemaDeadlockRetryDelays = [...]time.Duration{
	100 * time.Millisecond,
	250 * time.Millisecond,
	500 * time.Millisecond,
	time.Second,
}

type schemaExecer interface {
	ExecContext(context.Context, string, ...any) (sql.Result, error)
}

func isPostgresDeadlock(err error) bool {
	var pgErr *pgconn.PgError
	return errors.As(err, &pgErr) && pgErr.Code == "40P01"
}

func applySchemaWithRetry(ctx context.Context, execer schemaExecer, sleep func(time.Duration)) error {
	for attempt := 0; ; attempt++ {
		if _, err := execer.ExecContext(ctx, schemaSQL); err != nil {
			if !isPostgresDeadlock(err) || attempt >= len(schemaDeadlockRetryDelays) {
				return err
			}
			sleep(schemaDeadlockRetryDelays[attempt])
			continue
		}
		return nil
	}
}

// withSchemaMigrationLock pins the session-level lock to one checked-out
// connection. Running pg_advisory_lock through *sql.DB is incorrect because a
// later schema or unlock call may use a different pooled PostgreSQL session.
func withSchemaMigrationLock(ctx context.Context, sqlDB *sql.DB, action func(*sql.Conn) error) (err error) {
	conn, err := sqlDB.Conn(ctx)
	if err != nil {
		return err
	}
	defer conn.Close()
	if _, err := conn.ExecContext(ctx, `SELECT pg_advisory_lock($1)`, schemaMigrationLockKey); err != nil {
		return fmt.Errorf("advisory lock: %w", err)
	}
	defer func() {
		if _, unlockErr := conn.ExecContext(context.Background(), `SELECT pg_advisory_unlock($1)`, schemaMigrationLockKey); unlockErr != nil && err == nil {
			err = fmt.Errorf("advisory unlock: %w", unlockErr)
		}
	}()
	return action(conn)
}

// coordinateWithSchemaMigration makes long, multi-table archive transactions
// mutually exclusive with startup DDL while allowing ordinary runtime queries
// to continue normally.
func coordinateWithSchemaMigration(tx *sql.Tx) error {
	if _, err := tx.Exec(`SELECT pg_advisory_xact_lock($1)`, schemaMigrationLockKey); err != nil {
		return fmt.Errorf("coordinate with schema migration: %w", err)
	}
	return nil
}

// DSN resolves the PostgreSQL connection string and reports where it came from.
// Precedence: env ARTEX_PG_DSN > config file (config.json). There is no
// built-in default — it errors if neither source is configured.
func DSN() (dsn, source string, err error) {
	return config.PostgresDSN()
}

// DB wraps the shared *sql.DB. PG handles its own connection pool + concurrency
// (MVCC), so unlike the old SQLite store there is no process-wide write mutex.
type DB struct{ *sql.DB }

// ensureDatabase connects to the postgres system database and creates the target
// database if it does not exist. dsn must be a postgres:// URL.
func ensureDatabase(dsn string) error {
	u, err := url.Parse(dsn)
	if err != nil {
		return nil // unparseable DSN — let the normal Open fail with a clear error
	}
	dbName := strings.TrimPrefix(u.Path, "/")
	if dbName == "" || dbName == "postgres" {
		return nil
	}
	// connect to the postgres maintenance database instead
	adminDSN := *u
	adminDSN.Path = "/postgres"
	admin, err := sql.Open("pgx", adminDSN.String())
	if err != nil {
		return nil // best-effort; let Open surface the real error
	}
	defer admin.Close()
	if err := admin.Ping(); err != nil {
		return nil
	}
	var exists bool
	_ = admin.QueryRow(`SELECT true FROM pg_database WHERE datname=$1`, dbName).Scan(&exists)
	if !exists {
		if _, err := admin.Exec(`CREATE DATABASE "` + dbName + `"`); err != nil {
			return fmt.Errorf("create database %q: %w", dbName, err)
		}
	}
	return nil
}

// Open connects, applies the schema (idempotent), and seeds builtin rows.
func Open(dsn string) (*DB, error) {
	if err := ensureDatabase(dsn); err != nil {
		return nil, err
	}
	sqlDB, err := sql.Open("pgx", dsn)
	if err != nil {
		return nil, err
	}
	// database/sql secara default tidak membatasi jumlah koneksi: saat tidak ada koneksi idle di pool, koneksi baru akan selalu dibuat, sampai
	// mentok di max_connections PostgreSQL (default 100) baru ditolak, sehingga query di saat beban tinggi akan mendapat
	// **error** seperti `FATAL: sorry, too many clients already`. Setelah dibatasi, query yang melebihi batas akan
	// mengantre menunggu koneksi idle, pada beban yang sama ini jadi melambat bukan error, pemanggil tidak perlu lagi membedakan
	// antara "tidak terbaca" dan "tidak ada". maxOpenConns harus menyisakan ruang untuk psql / reset-password.sh dan
	// kemungkinan instance lain yang berjalan bersamaan; jika max_connections pernah diturunkan, nilai ini juga harus ikut diturunkan.
	sqlDB.SetMaxOpenConns(maxOpenConns)
	sqlDB.SetMaxIdleConns(maxOpenConns)
	sqlDB.SetConnMaxLifetime(30 * time.Minute)
	sqlDB.SetConnMaxIdleTime(5 * time.Minute)
	if err := sqlDB.Ping(); err != nil {
		sqlDB.Close()
		return nil, fmt.Errorf("ping postgres (%s): %w", config.Redact(dsn), err)
	}
	d := &DB{sqlDB}
	// pgx runs multi-statement Exec via the simple protocol when there are no args.
	// Keep the dedicated lock connection checked out until both DDL and seeding
	// finish so concurrent application instances cannot initialize out of order.
	err = withSchemaMigrationLock(context.Background(), sqlDB, func(conn *sql.Conn) error {
		if err := applySchemaWithRetry(context.Background(), conn, time.Sleep); err != nil {
			return fmt.Errorf("apply schema: %w", err)
		}
		if err := d.seedBuiltins(); err != nil {
			return fmt.Errorf("seed builtins: %w", err)
		}
		return nil
	})
	if err != nil {
		sqlDB.Close()
		return nil, err
	}
	return d, nil
}

// builtinAgent describes one of the fixed agents and its prompt-variable catalog.
type builtinAgent struct {
	key, name, role, desc string
	vars                  []promptVar
		interactiveShell      bool // Saklar default interactive shell saat baris dibuat; ON CONFLICT tidak menimpa saklar manual pengguna setelahnya
		runSeconds            *int // Batas wall-clock satu kali run saat baris dibuat (detik); nil=pakai default seed (1200), 0=tanpa batas waktu
}

type promptVar struct{ name, desc, example, source string }

// intp mengembalikan pointer dari v, dipakai untuk memberi nilai eksplisit pada field opsional builtinAgent (seperti runSeconds).
func intp(v int) *int { return &v }

// builtinAgents mirrors docs §5(a). Tool bawaan tidak disimpan ke database; di sini hanya melakukan seed agent dan katalog variabel.
// Catatan: interactive shell default untuk planner/worker/mainagent/auto diatur oleh blok interactive_shell_default_v1
// di bawah, yang menyeragamkan nilainya ke true (tetap menghormati toggle manual setelahnya); interactiveShell di sini hanya untuk agent baru yang memang perlu "aktif sejak baris dibuat".
var builtinAgents = []builtinAgent{
	{"goals", "Dekomposisi Tujuan", "goals", "Memecah tujuan tugas pentest menjadi beberapa sub-tujuan yang independen dan dapat diverifikasi.", []promptVar{
		{"EngagementDescription", "Deskripsi tugas (target pengujian/latar belakang)", "Uji situs example.com", "exploration"},
		// Now adalah variabel runtime global (lihat server.globalPromptVars), tidak lagi didefinisikan ulang
		// di katalog masing-masing agent, jika tidak withGlobalVars akan bentrok nama saat menambahkannya.
	}, false, nil},
	{"planner", "Perencanaan", "planner", "Membaca situasi, menentukan tujuan, dan hanya menambahkan intent eksplorasi baru ketika benar-benar ada arah yang belum tercakup (satu siklus perencanaan per tugas).", []promptVar{
		{"Goal", "Tujuan utama tugas", "Mendapatkan akses admin example.com", "exploration"},
		{"AssetSummary", "Ringkasan jumlah aset/distribusi tipe (opsional)", "domain:3 ip:5 site:2", "distilled"},
	}, false, nil},
	{"mainagent", "Utama", "main", "Antarmuka manusia-agent: mengamati progres, mengubah maksud pengguna menjadi hint atau intent berprioritas tinggi.", []promptVar{
		{"Goal", "Tujuan tugas saat ini", "Mendapatkan akses admin example.com", "exploration"},
		{"AssetSummary", "Ringkasan situasi awal (opsional)", "domain:3 ip:5", "distilled"},
		{"FindingsSummary", "Ringkasan temuan yang sudah terkonfirmasi (opsional)", "high:1 medium:2", "distilled"},
	}, false, nil},
	{"worker", "Eksekusi", "worker", "Mengambil satu intent untuk dieksekusi, menulis kembali fakta/temuan yang ditemukan ke knowledge graph, lalu berhenti.", []promptVar{
		{"ProxyAddr", "Alamat proxy perekaman (menggerakkan teks kondisional if)", "127.0.0.1:8080", "runtime"},
		{"WorkerName", "Identitas diri worker (opsional)", "worker-1", "runtime"},
	}, false, nil},
	// Auto: agent bawaan "operasi platform". Tidak ikut dalam loop orkestrasi pentest, dijalankan dari halaman chat, mengoperasikan platform lewat tool.
	{"auto", "Auto", "assistant", "Asisten operasi platform: mengelola tugas (membuat/melihat/menjeda/memberi hint) dan aset dengan tool, serta dapat membuat/mengubah skill, tool kustom, dan MCP.", nil, false, nil},
	// Pentest: agent bawaan "pentest independen". Dijalankan dari halaman chat, satu agent menjalani seluruh rantai pentest sendirian dari reconnaissance sampai penutupan, merencanakan, mengeksekusi, dan memverifikasi sendiri. Interactive shell aktif secara default.
	{"pentest", "Pentest", "assistant", "Agent pentest independen: satu agent menjalani seluruh rantai mulai dari reconnaissance, mencari attack surface, eksploitasi mendalam, verifikasi, sampai penutupan, merencanakan sendiri, mengeksekusi sendiri, dan memverifikasi sendiri secara adversarial.", nil, true, intp(0)},
}

// seedBuiltins inserts the fixed built-in agents and their variable catalog (idempotent).
func (d *DB) seedBuiltins() error {
	for _, a := range builtinAgents {
		var agentID int64
		err := d.QueryRow(`
INSERT INTO agents(key, name, description, role, builtin, enabled, interactive_shell, run_seconds)
VALUES ($1, $2, NULLIF($3,''), $4, true, true, $5, COALESCE($6, 1200))
ON CONFLICT (key) DO UPDATE SET name = EXCLUDED.name, description = EXCLUDED.description
RETURNING id`, a.key, a.name, a.desc, a.role, a.interactiveShell, a.runSeconds).Scan(&agentID)
		if err != nil {
			return fmt.Errorf("agent %s: %w", a.key, err)
		}
		for _, v := range a.vars {
			if _, err := d.Exec(`
INSERT INTO agent_prompt_vars(agent_id, var_name, description, example, source)
VALUES ($1, $2, $3, $4, $5)
ON CONFLICT (agent_id, var_name) DO UPDATE
  SET description = EXCLUDED.description, example = EXCLUDED.example, source = EXCLUDED.source`,
				agentID, v.name, v.desc, v.example, v.source); err != nil {
				return fmt.Errorf("agent %s var %s: %w", a.key, v.name, err)
			}
		}
	}
	// Drop catalog entries for variables that were renamed, so the white-list no
	// longer advertises a name templates can't resolve (EngagementTitle→Description).
		// Setelah 'Now' dinaikkan dari katalog masing-masing agent menjadi variabel runtime global, database lama di goals masih menyisakan satu 'Now'
		// yang akan bentrok nama dengan item global (key duplikat di daftar variabel frontend); dibersihkan sekalian di sini.
	if _, err := d.Exec(`DELETE FROM agent_prompt_vars WHERE var_name IN ('EngagementTitle', 'CoverageGaps', 'Now')`); err != nil {
		return fmt.Errorf("cleanup renamed vars: %w", err)
	}
	// Default-on interactive_shell for the runtime agents (planner/worker/mainagent/auto)
	// ONCE — respects a later user toggle-off (guarded by a settings flag). goals(one-shot
	// decomposer) stays off. Runs after the column exists (schema applied before seed).
	if v, _, _ := d.GetSetting("interactive_shell_default_v1"); v != "true" {
		if _, err := d.Exec(`UPDATE agents SET interactive_shell=true WHERE key IN ('planner','worker','mainagent','auto')`); err != nil {
			return fmt.Errorf("seed interactive_shell defaults: %w", err)
		}
		_ = d.SetSetting("interactive_shell_default_v1", "true")
	}
		// Seed the built-in browser (Playwright) MCP once, DISABLED by default (diaktifkan sendiri oleh pengguna
		// saat diperlukan), no proxy by default. The traffic-capture toggle injects/strips
	// the recording proxy + CA at runtime (server.Manager.syncBrowserMCPProxy).
	// Insert only if absent so we never clobber user edits (args/env/enabled/
	// visibility) on restart.
	if _, err := d.Exec(`
INSERT INTO mcp_servers(name, transport, command, args, env, enabled)
VALUES ('browser', 'stdio', 'npx', $1, '{}', false)
ON CONFLICT (name) DO NOTHING`,
		`["@playwright/mcp","--headless"]`); err != nil {
		return fmt.Errorf("seed browser mcp: %w", err)
	}
	// NOTE: the placeholder ScopeSentry data-source MCP (empty URL + empty X-API-Key,
	// disabled) is seeded directly in schema.sql §F so a raw `psql < schema.sql` init
	// also gets it. schema.sql is Exec'd on every startup, so it stays idempotent.
	if err := d.seedBuiltinSkillVisibility(); err != nil {
		return fmt.Errorf("seed skill visibility: %w", err)
	}
	if err := d.seedDefaultInterceptRules(); err != nil {
		return fmt.Errorf("seed intercept rules: %w", err)
	}
	if err := d.seedDefaultInterceptRulesV2(); err != nil {
		return fmt.Errorf("seed intercept rules v2: %w", err)
	}
	if err := d.seedDefaultInterceptRulesV3(); err != nil {
		return fmt.Errorf("seed intercept rules v3: %w", err)
	}
	if err := d.seedDefaultAssetInterceptRules(); err != nil {
		return fmt.Errorf("seed asset intercept rules: %w", err)
	}
	return nil
}

// seedDefaultAssetInterceptRules inserts the built-in asset blocklist (fuzzy
// domain matches for government / education sites) once on first startup. Gated
// by a settings flag so a user's later disable/delete is never resurrected on
// restart — same policy as the intercept-rule seed.
func (d *DB) seedDefaultAssetInterceptRules() error {
	if v, _, _ := d.GetSetting("asset_intercept_default_rules_v1"); v == "done" {
		return nil
	}
	rules := []struct {
		kind    string
		pattern string
		note    string
	}{
		{"fuzzy_domain", ".gov", "[Bawaan] Situs pemerintah (.gov)"},
		{"fuzzy_domain", ".gov.cn", "[Bawaan] Situs pemerintah (.gov.cn)"},
		{"fuzzy_domain", ".edu", "[Bawaan] Situs pendidikan (.edu)"},
		{"fuzzy_domain", ".edu.cn", "[Bawaan] Situs pendidikan (.edu.cn)"},
	}
	for _, r := range rules {
		if _, err := d.Exec(`
INSERT INTO asset_intercept_rules(enabled, kind, pattern, note, builtin)
VALUES (true, $1, $2, $3, true)
ON CONFLICT DO NOTHING`, r.kind, r.pattern, r.note); err != nil {
			return fmt.Errorf("asset rule %q: %w", r.pattern, err)
		}
	}
	return d.SetSetting("asset_intercept_default_rules_v1", "done")
}

// builtinSkillVisibility maps a shipped skill's directory name → the built-in
// agent keys that should see it by default. The skill FILES themselves live on the
// filesystem (SkillDir, loaded by norma at runtime); DB only carries this visibility
// binding. Skills omitted here (e.g. playwright-cli, scopesentry) ship invisible by
// default — the user turns them on per-agent when needed. scopesentry additionally
// declares `mcps: ScopeSentry`, which only takes effect once it's made visible and
// that MCP is enabled/configured.
var builtinSkillVisibility = map[string][]string{
	"api-recon": {"auto", "pentest", "worker"},
}

// seedBuiltinSkillVisibility binds the shipped built-in skills to their default
// agents. Insert-if-absent (ON CONFLICT DO NOTHING) so a user's later toggle-off is
// never resurrected on restart — matches the browser-MCP / intercept-rule seed policy.
func (d *DB) seedBuiltinSkillVisibility() error {
	for skillName, agentKeys := range builtinSkillVisibility {
		for _, key := range agentKeys {
			if _, err := d.Exec(`
INSERT INTO agent_skill_visibility(agent_id, skill_name, enabled)
SELECT id, $2, true FROM agents WHERE key=$1
ON CONFLICT (agent_id, skill_name) DO NOTHING`, key, skillName); err != nil {
				return fmt.Errorf("skill %s → agent %s: %w", skillName, key, err)
			}
		}
	}
	return nil
}

// seedDefaultInterceptRules inserts built-in safety intercept rules once on
// first startup. The seed is gated by a settings flag so user edits (disable,
// delete, re-order) are never overwritten on subsequent restarts.
func (d *DB) seedDefaultInterceptRules() error {
	if v, _, _ := d.GetSetting("intercept_default_rules_v1"); v == "done" {
		return nil
	}
	type rule struct {
		name     string
		target   string // tool_name | tool_input
		typ      string // string | regex
		pattern  string
		action   string
		message  string
		priority int
	}
	rules := []rule{
		// ── Perintah destruktif sistem (priority 100) ──────────────────────────────────
		{
			name:     "[Bawaan] Hapus paksa rekursif rm -rf",
			target:   "tool_input",
			typ:      "regex",
			pattern:  `(?i)\brm\b.{0,80}(?:-[a-z]*r[a-z]*f[a-z]*|-[a-z]*f[a-z]*r[a-z]*|--recursive|--no-preserve-root)`,
			action:   "deny",
			message:  "Eksekusi hapus paksa rekursif (rm -rf / rm --recursive) dilarang, dapat merusak sistem atau lingkungan target secara permanen",
			priority: 100,
		},
		{
			name:     "[Bawaan] Hapus direktori kritis sistem",
			target:   "tool_input",
			typ:      "regex",
			pattern:  `\brm\b[^"'\n]{0,60}["'\s](/|/etc|/bin|/usr|/boot|/var|/lib|/sys|/proc|/dev|/sbin|/root)`,
			action:   "deny",
			message:  "Penghapusan path kritis sistem dilarang",
			priority: 100,
		},
		{
			name:     "[Bawaan] Format disk mkfs",
			target:   "tool_input",
			typ:      "regex",
			pattern:  `\bmkfs\b`,
			action:   "deny",
			message:  "Format disk (mkfs) dilarang",
			priority: 100,
		},
		{
			name:     "[Bawaan] Timpa perangkat disk dd",
			target:   "tool_input",
			typ:      "regex",
			pattern:  `\bdd\b[^|\n]{0,100}\bof=\s*/dev/[a-zA-Z]`,
			action:   "deny",
			message:  "Penggunaan dd untuk menimpa perangkat disk dilarang",
			priority: 100,
		},
		{
			name:     "[Bawaan] Fork bomb",
			target:   "tool_input",
			typ:      "regex",
			pattern:  `:\(\)\s*\{[^}]*:\|:`,
			action:   "deny",
			message:  "Eksekusi fork bomb dilarang",
			priority: 100,
		},
		{
			name:     "[Bawaan] Shutdown / restart",
			target:   "tool_input",
			typ:      "regex",
			pattern:  `\b(?:shutdown|reboot|halt|poweroff|init\s+[06])\b`,
			action:   "deny",
			message:  "Eksekusi perintah shutdown atau restart dilarang",
			priority: 100,
		},
		{
			name:     "[Bawaan] Matikan semua proses",
			target:   "tool_input",
			typ:      "regex",
			pattern:  `\bkill\s+-9\s+-1\b|\bkillall\s+-9\b`,
			action:   "deny",
			message:  "kill -9 -1 atau killall -9 (mematikan semua proses) dilarang",
			priority: 100,
		},
		{
			name:     "[Bawaan] Hapus permanen disk shred / wipe",
			target:   "tool_input",
			typ:      "regex",
			pattern:  `\b(?:shred|wipe)\b[^|\n]{0,80}/dev/[a-zA-Z]`,
			action:   "deny",
			message:  "Eksekusi shred/wipe pada perangkat disk dilarang",
			priority: 100,
		},
		{
			name:     "[Bawaan] Kosongkan aturan firewall",
			target:   "tool_input",
			typ:      "regex",
			pattern:  `\biptables\s+(?:-F|--flush)\b|\bnft\s+flush\s+ruleset\b`,
			action:   "deny",
			message:  "Pengosongan aturan firewall (iptables -F / nft flush) dilarang",
			priority: 100,
		},
		// ── Operasi destruktif database (priority 90) ─────────────────────────────────
		{
			name:     "[Bawaan] SQL DROP DATABASE / TABLE / SCHEMA",
			target:   "tool_input",
			typ:      "regex",
			pattern:  `(?i)\bDROP\s+(?:DATABASE|TABLE|SCHEMA|INDEX|VIEW|TABLESPACE|USER|ROLE)\b`,
			action:   "deny",
			message:  "Eksekusi operasi DROP dilarang, dapat menghancurkan objek database secara ireversibel",
			priority: 90,
		},
		{
			name:     "[Bawaan] SQL TRUNCATE",
			target:   "tool_input",
			typ:      "regex",
			pattern:  `(?i)\bTRUNCATE\s+(?:TABLE\s+)?\w`,
			action:   "deny",
			message:  "Eksekusi TRUNCATE dilarang, dapat mengosongkan seluruh data pada tabel",
			priority: 90,
		},
		{
			name:     "[Bawaan] MongoDB drop / dropDatabase",
			target:   "tool_input",
			typ:      "regex",
			pattern:  `(?i)\.(?:dropDatabase|dropCollection|drop)\s*\(`,
			action:   "deny",
			message:  "Eksekusi operasi drop MongoDB dilarang",
			priority: 90,
		},
		{
			name:     "[Bawaan] Redis FLUSHALL / FLUSHDB",
			target:   "tool_input",
			typ:      "regex",
			pattern:  `(?i)\b(?:FLUSHALL|FLUSHDB)\b`,
			action:   "deny",
			message:  "Eksekusi Redis FLUSHALL / FLUSHDB dilarang, dapat mengosongkan seluruh data cache",
			priority: 90,
		},
		// ── Request destruktif HTTP (priority 80) ──────────────────────────────────
		// Tiga cara umum agent mengirim request DELETE:
		//   1. curl -X DELETE / --request DELETE (dijalankan langsung oleh tool Bash atau ditulis ke skrip)
		//   2. Metode .delete() client HTTP Python
		//   3. method: 'DELETE' / method="DELETE" pada skrip JS/umum
		{
			name:     "[Bawaan] curl / wget mengirim request DELETE",
			target:   "tool_input",
			typ:      "regex",
			pattern:  `(?i)\bcurl\b[^|\n&;"]{0,300}(?:-X\s*DELETE|--request\s+DELETE|-XDELETE)|\bwget\b[^|\n&;"]{0,300}--method[=\s]+DELETE`,
			action:   "deny",
			message:  "Pengiriman request HTTP DELETE melalui curl/wget dilarang, dapat menghapus data sistem target",
			priority: 80,
		},
		{
			name:     "[Bawaan] Client HTTP Python DELETE (requests/httpx/aiohttp)",
			target:   "tool_input",
			typ:      "regex",
			pattern:  `(?i)\b(?:requests|httpx|aiohttp|urllib\.request)\.delete\s*\(|session\.delete\s*\(|client\.delete\s*\(`,
			action:   "deny",
			message:  "Penggunaan client HTTP Python untuk mengirim request DELETE dilarang",
			priority: 80,
		},
		{
			name:     "[Bawaan] Deklarasi metode HTTP DELETE dalam skrip (JS/umum)",
			target:   "tool_input",
			typ:      "regex",
			pattern:  `(?i)axios\.delete\s*\(|method\s*[:=]\s*['"]DELETE['"]`,
			action:   "deny",
			message:  "Deklarasi dan pengiriman request HTTP DELETE dalam skrip dilarang",
			priority: 80,
		},
		{
			name:     "[Bawaan] Path endpoint pengosongan/penghapusan massal",
			target:   "tool_input",
			typ:      "regex",
			pattern:  `(?i)/(?:clear|wipe|flush|purge|truncate|drop|destroy|factory[-_]reset|reset[-_]all)(?:[/?#"'\s]|$)`,
			action:   "deny",
			message:  "Pemanggilan endpoint pengosongan atau penghancuran massal (/clear /wipe /flush /purge dll) dilarang",
			priority: 80,
		},
	}
	for _, r := range rules {
		if _, err := d.Exec(`
INSERT INTO intercept_rules(name, enabled, priority, match_target, match_type, pattern, action, message, timeout_enabled, timeout_seconds, timeout_action)
VALUES ($1, true, $2, $3, $4, $5, $6, $7, false, 60, 'deny')
ON CONFLICT DO NOTHING`,
			r.name, r.priority, r.target, r.typ, r.pattern, r.action, r.message,
		); err != nil {
			return fmt.Errorf("rule %q: %w", r.name, err)
		}
	}
	return d.SetSetting("intercept_default_rules_v1", "done")
}

// seedDefaultInterceptRulesV2 migrates the two safety patterns that used to be
// hard-coded in guard.go (destructive shell + data-exfil pipe) into ordinary
// intercept rules. Gated by its own flag so it also lands on DBs that already ran
// v1. Unlike the old guard.go floor, these are plain [Bawaan] rules, the user can
// disable or delete them. The exfil rule ships DISABLED by default (its
// curl/wget/nc pipe pattern mis-fires on legitimate CTF/pentest reverse-shell and
// data-transfer pipes); enable it manually when exfil gating is actually wanted.
func (d *DB) seedDefaultInterceptRulesV2() error {
	if v, _, _ := d.GetSetting("intercept_default_rules_v2"); v == "done" {
		return nil
	}
	rules := []struct {
		name     string
		pattern  string
		action   string
		message  string
		enabled  bool
		priority int
	}{
		{
			name:     "[Bawaan] Perintah destruktif sistem",
			pattern:  `(?i)\b(rm\s+-rf\s+/|mkfs|dd\s+if=|:\(\)\s*\{|shutdown|reboot|>\s*/dev/sd)`,
			action:   "deny",
			message:  "Perintah destruktif ditolak (rm -rf / / mkfs / dd / fork bomb / shutdown restart / timpa perangkat disk)",
			enabled:  true,
			priority: 100,
		},
		{
			name:     "[Bawaan] Pipa eksfiltrasi data",
			pattern:  `(?i)(curl|wget|nc|ncat)\b[^|]*\b(\|\s*(curl|wget|nc))`,
			action:   "deny",
			message:  "Diduga pipa eksfiltrasi data ditolak (output perintah diteruskan keluar lewat curl/wget/nc)",
			enabled:  false,
			priority: 80,
		},
	}
	for _, r := range rules {
		if _, err := d.Exec(`
INSERT INTO intercept_rules(name, enabled, priority, match_target, match_type, pattern, action, message, timeout_enabled, timeout_seconds, timeout_action)
VALUES ($1, $2, $3, 'tool_input', 'regex', $4, $5, $6, false, 60, 'deny')
ON CONFLICT DO NOTHING`,
			r.name, r.enabled, r.priority, r.pattern, r.action, r.message,
		); err != nil {
			return fmt.Errorf("rule %q: %w", r.name, err)
		}
	}
	return d.SetSetting("intercept_default_rules_v2", "done")
}

// seedDefaultInterceptRulesV3 adds the delete-endpoint path rule. The v1 HTTP rules
// only catch the DELETE *method* (curl -X DELETE, requests.delete(, method:'DELETE'),
// and v1's path rule covers only /clear /wipe /flush /purge /truncate /drop /destroy
// /factory-reset /reset-all — so a plain `curl 'http://t/api/user/delete?id=1'` (a
// delete endpoint reached with GET/POST, which is how most web apps expose deletion)
// slipped through every built-in rule. Own flag so it also lands on DBs that already
// ran v1/v2, where editing the v1 seed would have no effect.
//
// The pattern deliberately requires a separator after the verb so /delivery,
// /details, /delta and /delegate do not match, while /deleteAll, /delete_user and
// /delete-user do. destroy is re-covered here because v1's rule does not allow a
// suffix (/destroyAll was missed).
//
// Exported as a package const only so the seeded regex is unit-testable without a DB.
const deleteEndpointPathPattern = `(?i)/(?:(?:delete|remove|unlink|erase|destroy)[-\w]*|del)(?:[/?#"'\s]|$)`

func (d *DB) seedDefaultInterceptRulesV3() error {
	if v, _, _ := d.GetSetting("intercept_default_rules_v3"); v == "done" {
		return nil
	}
	const name = "[Bawaan] Path endpoint penghapusan"
	if _, err := d.Exec(`
INSERT INTO intercept_rules(name, enabled, priority, match_target, match_type, pattern, action, message, timeout_enabled, timeout_seconds, timeout_action)
SELECT $1, true, 80, 'tool_input', 'regex', $2, 'deny', $3, false, 60, 'deny'
WHERE NOT EXISTS (SELECT 1 FROM intercept_rules WHERE name = $1)`,
		name,
		deleteEndpointPathPattern,
		"Pemanggilan endpoint penghapusan (/delete /remove /unlink /erase dll) dilarang, apa pun metode HTTP yang dipakai, karena pada kebanyakan aplikasi endpoint penghapusan bisa dipicu lewat GET/POST dan tetap benar-benar menghapus data target",
	); err != nil {
		return fmt.Errorf("rule %q: %w", name, err)
	}
	return d.SetSetting("intercept_default_rules_v3", "done")
}
