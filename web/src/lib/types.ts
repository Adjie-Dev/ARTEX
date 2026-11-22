// ARTEX domain model — types used across the UI.
// Derived from the functional spec (section 7: bentuk data kunci).

export type TaskStatus = "created" | "queued" | "running" | "paused" | "done" | "failed" | "timeout";
export type EngineMode = "exploring" | "paused" | "stalled" | "idle";

export interface Task {
  id: string;
  name?: string; // Nama tugas opsional; kosong/default = tanpa nama, saat ditampilkan jatuh kembali ke deskripsi
  category_id?: number;
  category_name?: string;
  pinned?: boolean;
  pinned_at?: string | null;
  description: string;
  goal: string;
  status: TaskStatus;
  created_at: string;
  created_unix?: number; // created_at as unix seconds (run-duration calc)
  completed_at?: string; // RFC3339 finish time (done/failed); "" if unfinished
  completed_unix?: number; // completed_at as unix seconds (0/undef if unfinished)
  last_activity_unix?: number; // unix seconds of the last activity (0/undef if none)
  paused?: boolean;
  queued?: boolean;
  active?: boolean;
  in_flight?: number;
  findings?: { critical: number; high: number; medium: number; low: number }; // Jumlah kerentanan yang tercatat (dikelompokkan menurut tingkat keparahan)
  last_activity?: string;
  stalled?: boolean;
  goals_total?: number;
  goals_met?: number;
  engine_mode?: EngineMode;
  tokens?: TokenTotal; // whole-task token consumption
  llm_profile_id?: number; // LLM profile used; absent = default profile
  llm_profile_ids?: number[]; // ordered task-level failover chain
  active_llm_profile_id?: number; // profile used by the next LLM call
  llm_failover_state?: "default" | "ready" | "chain_exhausted" | string;
  llm_failover_reason?: string;
  source_task_ids?: string[]; // directly related tasks inherited as read-only context
  archive_blocked_by_task_id?: string; // live direct dependent that must be archived first
  company_ids?: number[]; // associated company scopes; current company assets join the task at creation
  coverage_enabled?: boolean; // Saklar fitur cakupan aset (ditentukan saat pembuatan, default aktif); false = cakupan tidak dihitung/ditampilkan
}

export interface TaskCategory {
  id: number;
  name: string;
  task_count: number;
  created_at: string;
  updated_at: string;
}

export interface TaskTemplate {
  id: number;
  name: string;
  description: string;
  goal: string;
  category_id?: number | null; // Kategori preset; null/default = tidak ada
  intercept_rules?: AssetInterceptRuleInput[]; // Aturan Intersepsi/izin tingkat tugas yang sudah ditentukan sebelumnya
  created_at: string;
  updated_at: string;
}

export interface DeleteTaskOptions {
  delete_assets: boolean;
  delete_traffic: boolean;
  delete_files: boolean;
  delete_findings: boolean;
  delete_llm_records: boolean;
}

export interface DeleteTaskResult {
  deleted: string;
  assets_deleted: number;
  assets_detached: number;
  traffic_deleted: number;
  files_deleted: boolean;
  findings_deleted: number;
  llm_records_deleted: number;
  cleanup_warning?: string;
}

export type TaskArchiveState =
  | "archive_queued"
  | "archiving"
  | "archive_failed"
  | "ready"
  | "restore_queued"
  | "restoring"
  | "restore_failed"
  | "delete_queued"
  | "deleting"
  | "delete_failed";

export interface TaskArchiveTokenStats {
  calls?: number;
  input_tokens?: number;
  output_tokens?: number;
  cache_read_tokens?: number;
  cache_write_tokens?: number;
}

export interface TaskArchive {
  id: number;
  task_id: number;
  state: TaskArchiveState;
  phase: string;
  progress: number;
  error?: string;
  warnings?: string[];
  format_version: number;
  sha256?: string;
  original_size: number;
  compressed_size: number;
  task_name: string;
  task_description: string;
  task_goal: string;
  original_status: TaskStatus;
  category_id?: number;
  category_name?: string;
  source_task_ids: number[];
  remaining_timeout_seconds: number;
  data_counts: Record<string, number>;
  aggregate_stats: {
    tokens?: TaskArchiveTokenStats;
    skills?: Record<string, number>;
    tools?: Record<string, number>;
    findings?: Record<string, number>;
  };
  archived_at?: string;
  requested_at: string;
  created_at: string;
  updated_at: string;
}

export interface TaskArchivePage {
  items: TaskArchive[];
  total: number;
  page: number;
  size: number;
}

export interface ArchiveBatchItem {
  id: string;
  archive_id?: number;
  ok: boolean;
  queued: boolean;
  error?: string;
}

// ---- Asset graph (global, shared across tasks) ----
export type AssetType =
  | "company"
  | "domain"
  | "ip"
  | "port"
  | "service"
  | "site"
  | "endpoint"
  | "parameter"
  | "tech"
  | "credential"
  | "data";

export type NodeState = "observed" | "confirmed" | "tombstoned";

export interface AssetNode {
  id: string;
  type: AssetType;
  name: string;
  key: string; // nkey
  value?: string;
  company_id?: string; // Id aset perusahaan pemilik; kosong = belum ada pemilik
  state: NodeState;
  confidence: number; // 0..1
  attrs?: Record<string, unknown>;
  first_seen: string;
  last_seen: string;
}

export type AssetRel =
  | "owns"
  | "resolves"
  | "exposes"
  | "runs"
  | "serves"
  | "has_endpoint"
  | "has_param"
  | "fingerprinted"
  | "authenticates_as"
  | "reachable"
  | "has_subdomain";

export interface Edge {
  src: string;
  dst: string;
  rel: AssetRel | ExploreRel;
}

// Task asset view — server-side enriched, paginated.
export interface TaskAssetRef {
  id: string;
  name?: string;
  key: string;
  attrs?: Record<string, unknown>;
}

export interface TaskAssetItem extends AssetNode {
  techs?: TaskAssetRef[];
  auth?: TaskAssetRef[];
  params?: TaskAssetRef[];
}

export interface TaskAssetView {
  counts: Record<string, number>;
  total: number;
  items: TaskAssetItem[];
}

// ---- New unified asset model (new backend) ----
export type NewAssetType = "root_domain" | "ip" | "subdomain" | "app" | "service" | "endpoint";

export interface Asset {
  id: number;
  type: NewAssetType;
  company_id?: number;
  task_ids: number[];
  domain?: string;
  root_domain?: string;
  ip?: string;
  c_segment?: string;
  port?: number;
  icp?: string;
  bound_domains?: string[];
  open_ports?: { port: number; service?: string }[];
  record_type?: string;
  record_value?: string[] | string;
  bundle_id?: string;
  app_name?: string;
  category?: string;
  app_description?: string;
  app_icp?: string;
  url?: string;
  service_type?: string;
  service_name?: string;
  favicon_mmh3?: string;
  status_code?: number;
  content_length?: number;
  page_title?: string;
  technologies?: string[];
  auth?: Record<string, unknown>[];
  method?: string;
  params?: Record<string, unknown>[];
  extra?: Record<string, unknown>;
  last_seen: string;
  task_source?: string;
  task_source_summary?: string;
  task_source_node_id?: number;
}

export interface IntentAsset {
  intent_id: number | string;
  asset_id: number;
  type: NewAssetType;
  label: string;
  source: string;
  source_summary: string;
  source_node_id?: number;
  source_task_id: number;
  inherited: boolean;
}

export interface TaskAssetMutation {
  requested: number;
  attached: number;
  existing: number;
}

export interface TaskAssetScopeMutation {
  requested: number;
  assets_linked: number;
  assets_existing: number;
  scopes_added: number;
  scopes_existing: number;
}

// ---- Asset coverage graph (per task) ----
// Satu node pada grafik cakupan aset berbasis force-directed. Key bersifat unik: aset="a:<id>", perusahaan="c:<id>",
// domain root tanpa baris aset="r:<domain>". in_scope=false adalah node konteks abu-abu yang hanya dipakai untuk menghubungkan garis.
export interface CoverageGraphNode {
  key: string;
  kind: "company" | "root_domain" | "subdomain" | "ip" | "service" | "app" | "endpoint";
  label: string;
  tested: boolean;
  in_scope: boolean;
  asset_id?: number;
  company_id?: number;
  domain?: string;
  root_domain?: string;
  ip?: string;
  url?: string;
  port?: number;
  service_type?: string;
  app_name?: string;
  page_title?: string;
  status_code?: number;
}

export interface CoverageGraphEdge {
  src: string;
  dst: string;
}

export interface CoverageGraphData {
  nodes: CoverageGraphNode[];
  edges: CoverageGraphEdge[];
}

// Intent/fact/finding yang terkait dengan suatu aset pada grafik eksplorasi tugas ini (dipakai drawer node grafik cakupan).
export interface CoverageAssetRef {
  id: number;
  kind: string;
  state: string;
  summary: string;
  source_task_id?: string;
  inherited?: boolean;
}
export interface CoverageAssetRefs {
  intents: CoverageAssetRef[];
  facts: CoverageAssetRef[];
  findings: CoverageAssetRef[];
}

// ---- Workspace file manager (workDir) ----
export interface WorkspaceEntry {
  name: string;
  path: string; // workspace-relative, forward slashes
  dir: boolean;
  size: number;
  mtime: number; // unix millis
}
export interface WorkspaceListing {
  path: string;
  entries: WorkspaceEntry[];
}
export interface WorkspaceFile {
  path: string;
  size: number;
  binary: boolean;
  too_large?: boolean;
  content?: string;
}

// Satu baris cakupan pengujian tugas (denominator cakupan + batas otorisasi).
export interface TaskScopeRow {
  id: number;
  task_id: number;
  kind: "company" | "root_domain" | "subdomain" | "ip" | "cidr" | "icp" | "keyword";
  company_id?: number;
  company_name?: string; // Di-resolve backend lewat JOIN companies, hanya terisi saat kind=company
  domain?: string;
  net?: string;
  value?: string;
  source: "auto" | "agent" | "manual";
  reason?: string;
}

export type CompanyScopeKind = "domain" | "ip" | "cidr" | "icp" | "keyword";

// Aturan cakupan aset terstruktur yang dikirim saat menambahkan perusahaan baru.
export interface CompanyScopeRule {
  kind: CompanyScopeKind;
  value: string;
}

// Hasil penulisan cakupan aset. errors adalah baris yang tidak valid pada pengiriman ini; warnings adalah masalah data lama
// yang tidak terkait pengiriman ini tapi bisa membuat hasil kepemilikan tidak sesuai harapan (misalnya aset yang kolom ip-nya berisi hostname).
export interface CompanyScopeMutation {
  added: number;
  skipped: number;
  invalid: number;
  errors?: string[];
  warnings?: string[];
}

// Satu baris aturan cakupan aset perusahaan (sumber kebenaran tunggal untuk kepemilikan).
export interface ScopeRow {
  id: number;
  company_id: number;
  kind: CompanyScopeKind;
  domain?: string; // Terisi saat kind=domain
  net?: string; // Terisi saat kind=ip|cidr
  value?: string; // Saat kind=icp|keyword, kemungkinan dikembalikan langsung oleh backend
  raw: string; // Input asli dari pengguna, dipakai untuk ditampilkan dan diisi ulang
  reason?: string;
}

// Perusahaan: node aset dengan type=company, ditambah ikon, jumlah aset, dan aturan cakupan aset.
export interface Company {
  id: number;
  name: string;
  logo?: string; // URL ikon remote; jika kosong, frontend memakai huruf pertama nama
  asset_count: number;
  scope?: ScopeRow[];
}

// ---- Exploration graph (per task) ----
export type ExploreKind = "task" | "begin" | "goal" | "intent" | "fact" | "finding" | "hint" | "digest";
export type GoalState = "open" | "met" | "abandoned";
export type IntentState = "open" | "running" | "paused" | "done" | "blocked" | "exhausted" | "stopped";
export type FindingState = "confirmed" | "dismissed";
export type HintState = "active" | "consumed";
export type ExploreRel = "spawns" | "derived_from" | "yields" | "proves" | "covers";

export interface TaskNode {
  id: string;
  type: ExploreKind;
  payload?: string;
  priority: number; // 0..10
  state: string; // GoalState | IntentState | FindingState | HintState
  origin: string;
  ts: string;
  source_task_id?: string;
  inherited?: boolean;
  delete_reason?: string; // Alasan penghapusan saat intent dihapus secara soft (state='deleted')
}

// Satu halaman papan siaran: node yang dipaginasi berdasarkan urutan pembuatan, ditambah edge yang
// terlibat di halaman ini dan node di ujung lain edge tersebut (refs, diindeks berdasarkan id), sehingga tiap siaran bisa menjelaskan dengan jelas "dari mana asalnya, apa yang dihasilkan" tanpa perlu menarik seluruh grafik.
export interface ExplorationNodePage {
  items: TaskNode[];
  total: number;
  page: number;
  size: number;
  edges: Edge[];
  refs: Record<string, TaskNode>;
  // Id node -> aset yang ditambatkan pada node tersebut (ditampilkan sekaligus saat papan siaran dibuka, mencakup node di halaman ini beserta tetangganya).
  assets: Record<string, FindingAsset[]>;
}

export interface ExplorationNodeQuery {
  page?: number;
  size?: number;
  kinds?: ExploreKind[];
  states?: string[];
  q?: string;
  order?: "asc" | "desc";
}

// Goal yang dipakai kartu manajemen goal (backend sudah memecah payload menjadi text/vulnclass).
export interface TaskGoal {
  id: string;
  text: string;
  vulnclass?: string;
  state: string; // GoalState
  origin?: string;
  ts: string;
}

// Batasan operasi yang dipakai kartu manajemen batasan (allow=mengizinkan / deny=melarang).
export type ConstraintKind = "allow" | "deny";
export interface TaskConstraint {
  id: string;
  kind: ConstraintKind;
  text: string;
  origin?: string;
  ts?: string;
}

// ---- Findings ----
export type Severity = "critical" | "high" | "medium" | "low";

// Status penanganan kerentanan: Menunggu / Diproses / Terkonfirmasi / Sudah Ditangani / Sudah Diperbaiki / Positif Palsu / Diabaikan / Duplikat / Risiko Diterima.
export type FindingStatus =
  | "pending"
  | "in_progress"
  | "confirmed"
  | "resolved"
  | "fixed"
  | "false_positive"
  | "ignored"
  | "duplicate"
  | "risk_accepted";

// FindingAsset adalah aset yang terikat pada satu kerentanan (label sudah dirender sebelumnya oleh backend).
export interface FindingAsset {
  id: string;
  type: string;
  label: string;
}

export interface Finding {
  traffic_count?: number;
  evidence_version?: number;
  report_evidence_version?: number;
  report_stale?: boolean;
  id: string;
  finding_id?: string; // Id baris pada tabel findings terpisah, menjadi handle untuk update status (node lama dalam tugas mungkin tidak memilikinya)
  vulnclass: string;
  name?: string; // Nama kerentanan; jika kosong, tampilan jatuh kembali ke vulnclass
  severity: Severity;
  status: FindingStatus;
  summary: string;
  evidence: string;
  report?: string; // Laporan detail (Markdown); hanya dikembalikan oleh endpoint detail, kosong pada list
  intent_id?: string;
  param_id?: string;
  task_id?: string;
  task_description?: string;
  source_task_id?: string;
  inherited?: boolean;
  assets?: FindingAsset[];
  ts: string;
}

// FindingsPage adalah respons paginasi dari server untuk daftar temuan.
export interface FindingsPage {
  items: Finding[];
  total: number;
  page: number;
  page_size: number;
}

export interface FindingGroup {
  task_id: string | number | null;
  task_name?: string; // Nama tugas opsional; kosong/default = tanpa nama
  task_description: string;
  task_status: string;
  count: number;
  critical: number;
  high: number;
  medium: number;
  low: number;
  last_found_at: string;
}

export interface FindingGroupsPage {
  items: FindingGroup[];
  total: number;
  finding_total: number;
  page: number;
  page_size: number;
}

export interface FindingDeepenResponse {
  task_id: string;
  intent_id: string;
  state: IntentState;
  queued: boolean;
}

// FindingStats adalah agregasi seluruh tabel temuan (kartu statistik + dropdown tipe kerentanan), dihitung di server, tidak terpengaruh paginasi.
export interface FindingStats {
  total: number;
  pending: number;
  critical: number;
  high: number;
  medium: number;
  low: number;
  vulnclasses: string[];
  tasks: FindingTaskOption[];
}

// FindingTaskOption adalah satu item pada dropdown filter "per tugas" di halaman temuan: tugas yang punya kerentanan (deskripsi kosong berarti tugas sudah dihapus,
// frontend jatuh kembali menampilkan id) beserta jumlah kerentanannya.
export interface FindingTaskOption {
  id: string | number;
  name?: string; // Nama tugas opsional; kosong/default = tanpa nama
  description: string;
  count: number;
}

// FindingQuery adalah parameter paginasi/filter/urutan untuk daftar temuan.
export interface FindingQuery {
  page: number;
  pageSize: number;
  severity?: "all" | Severity;
  status?: "all" | FindingStatus;
  vulnclass?: string;
  task?: string; // Id tugas; "all"/kosong = tidak difilter berdasarkan tugas
  query?: string;
  sort?: "severity" | "time";
  // Key node pohon aset; memilih satu node berarti memilih seluruh subtree-nya. Kosong = tidak difilter berdasarkan aset.
  assetScope?: string;
}

// ---- Findings by asset (tampilan aset) ----
export type FindingAssetKind = "company" | "root_domain" | "subdomain" | "ip" | "service" | "app" | "endpoint" | "none";

// FindingAssetNode adalah satu node pada pohon aset. Key berbentuk a:<id> (aset), c:<id> (perusahaan),
// r:<domain> (domain root yang tidak punya baris aset di database), __none__ (tidak terkait aset).
export interface FindingAssetNode {
  key: string;
  parent?: string;
  kind: FindingAssetKind;
  label: string;
  asset_id?: number;
  company_id?: number;
  self: number; // Jumlah temuan yang langsung melekat pada aset ini
  total: number; // Termasuk turunannya, dihitung unik per temuan
  critical: number;
  high: number;
  medium: number;
  low: number;
  last_found_at: string;
}

export interface FindingAssetTree {
  nodes: FindingAssetNode[];
  finding_total: number;
  truncated: boolean;
  dropped_kinds?: string[];
}

// FINDING_UNASSIGNED_ASSET berpadanan dengan db.FindingUnassignedAsset di backend.
export const FINDING_UNASSIGNED_ASSET = "__none__";

// ---- Activity / sessions ----
export type ActivityKind =
  | "tool_use"
  | "tool_result"
  | "text"
  | "thinking"
  | "result"
  | "user"
  | "intent" // LLM-generated exploration objective leading a worker session (UI-synthesized)
  | "round" // planner round boundary marker (engine-emitted)
  | "usage" // live cumulative token usage (per model turn); not rendered
  | "llm_switch" // automatic/manual task-level LLM switch
  | "llm_failover" // task-level provider switch / chain exhaustion audit event
  | "intercept_request"; // user-approval request from the intercept layer

// ChatAttachment adalah satu file yang diunggah: path relatif terhadap direktori kerja sesi/tugas tersebut (yaitu CWD agent).
export interface ChatAttachment {
  name: string;
  path: string;
  size: number;
  abs?: string; // Path absolut (dikembalikan saat scope=staging untuk unggahan sementara; tuliskan ke deskripsi sebelum membuat tugas)
}

export interface Activity {
  seq: number;
  intent_id?: string;
  worker: string; // session owner: planner | mainagent | work#1 ...
  ts: string;
  kind: ActivityKind;
  tool?: string;
  tool_use_id?: string;
  is_error?: boolean;
  summary: string;
  detail?: string;
  metadata?: {
    llm_transition?: LLMTransition;
  };
  source_task_id?: string;
  inherited?: boolean;
  main_seg?: number; // main-agent conversation segment (present only on worker="mainagent" rows)
  // token usage (present only on kind='result')
  input_tokens?: number;
  output_tokens?: number;
  cache_read_tokens?: number;
  cache_write_tokens?: number;
}

export interface LLMAuditProfile {
  id: number;
  name: string;
  format: string;
  model: string;
}

export interface LLMTransition {
  mode: "automatic" | "manual" | "exhausted";
  reason: string;
  previous?: LLMAuditProfile;
  next?: LLMAuditProfile;
}

export interface TaskLLMResolution {
  profile_id?: number;
  name: string;
  format: string;
  model: string;
  source: "task_chain" | "agent_binding" | "global_profile" | "environment" | "global";
  available: boolean;
  reason?: string;
}

export interface TaskLLMResolutions {
  mainagent: TaskLLMResolution;
  planner: TaskLLMResolution;
  worker: TaskLLMResolution;
}

// ---- Agent triggers (penjadwalan P3, hanya agent kustom) ----
export interface AgentTrigger {
  id: number;
  agent_key: string;
  enabled: boolean;
  interval_sec: number; // Terjadwal: setiap N detik (0 = tidak terjadwal)
  on_finding: boolean; // Terpicu saat tugas mana pun menghasilkan finding
  on_goal_met: boolean; // Terpicu saat tugas mana pun mencapai goal
  on_task_timeout: boolean; // Terpicu saat tugas mana pun timeout
  on_tool_call: boolean; // Terpicu saat Tool yang dipilih dipanggil (eksekusi selesai)
  on_task_create: boolean; // Terpicu saat tugas mana pun dibuat
  interval_message: string; // Pesan pengguna terpisah untuk masing-masing kondisi pemicu
  finding_message: string;
  goal_message: string;
  task_timeout_message: string;
  tool_call_message: string;
  task_create_message: string;
  tool_names: string[]; // Key Tool yang dipilih untuk on_tool_call (minimal satu)
  last_fire?: string;
}

// ---- Conversations (chat page) ----
export interface ActiveFindingRetest {
  id: number;
  finding_id: string;
  conversation_id: number;
  status: "pending" | "running";
}

export interface FindingRetest {
  id: number;
  finding_id: number;
  conversation_id: number | null;
  status: "pending" | "running" | "completed" | "failed" | "stopped";
  verdict: "" | "reproduced" | "fixed" | "inconclusive";
  notes: string;
  summary: string;
  evidence: string;
  error: string;
  created_at: string;
  started_at: string | null;
  finished_at: string | null;
}

export interface Conversation {
  id: number;
  running?: boolean; // live server state, returned with the conversation list
  agent_key: string;
  title: string;
  llm_profile_id?: number;
  pinned?: boolean;
  pinned_at?: string | null;
  created_at: string;
  updated_at: string;
}

// ---- Backend logs (/logs page) ----
export interface LogLine {
  seq: number;
  db_id?: number; // server_logs.id; present for DB-persisted lines
  ts: string;
  level: "info" | "warn" | "error";
  tag: string;
  text: string;
}

export type SessionRole = "mainagent" | "planner" | "worker" | "system";
export type SessionStatus = "running" | "paused" | "done" | "blocked" | "exhausted" | "pending" | "stopped" | "deleted";

// Daily token aggregate bucket (GET /api/tokens/daily).
export interface DailyTokenBucket {
  date: string; // "YYYY-MM-DD"
  input_tokens: number;
  output_tokens: number;
  cache_read_tokens: number;
  cache_write_tokens: number;
}

// Per-worker token usage (GET /api/exploration/tokens).
export interface TokenUsage {
  worker: string;
  input_tokens: number;
  output_tokens: number;
  cache_read_tokens: number;
  cache_write_tokens: number;
}

export interface SessionTokenUsage {
  session: string;
  input_tokens: number;
  output_tokens: number;
  cache_read_tokens: number;
  cache_write_tokens: number;
}

export interface BatchControlItem {
  id: string;
  ok: boolean;
  status?: string;
  queued?: boolean;
  error?: string;
}

// Hasil per tugas dari perubahan kategori secara massal. Kegagalan hanya mungkin terjadi karena tugas sudah dihapus; penulisan kategorinya sendiri bersifat atomik.
export interface BatchCategoryItem {
  id: string;
  ok: boolean;
  error?: string;
}

// Whole-task (all agents) token aggregate.
export interface TokenTotal {
  input_tokens: number;
  output_tokens: number;
  cache_read_tokens: number;
  cache_write_tokens: number;
}

// Global per-profile token spend from the llm_usage ledger (GET /api/tokens/usage).
export interface ProfileUsage {
  profile_name: string;
  calls: number;
  tasks: number;
  input_tokens: number;
  output_tokens: number;
  cache_read_tokens: number;
  cache_write_tokens: number;
}

// One (profile, UTC day) token bucket for the dashboard's daily chart (new source).
export interface ProfileDayUsage {
  profile_name: string;
  date: string; // YYYY-MM-DD
  input_tokens: number;
  output_tokens: number;
  cache_read_tokens: number;
}

// Response of GET /api/tokens/usage — the dashboard's "new" (llm_usage) token view.
export interface UsageStats {
  by_profile: ProfileUsage[];
  daily: ProfileDayUsage[];
}

// Per-model token usage for one task (GET /api/llm/records/by-model), from the
// always-on llm_usage metering ledger. calls = number of LLM calls on this model.
export interface ModelTokenStat {
  model: string;
  calls: number;
  input_tokens: number;
  output_tokens: number;
  cache_read_tokens: number;
  cache_write_tokens: number;
}

export interface Session {
  id: string;
  role: SessionRole;
  title: string;
  status: SessionStatus;
  live: boolean;
  last_activity: string;
  intent_id?: string;
  source_task_id?: string;
  inherited?: boolean;
  seg?: number; // main-agent session: which conversation segment (0 = original)
}

// ---- Security ----
export interface AuditEntry {
  ts: string;
  tool: string;
  action: "allow" | "block";
  reason?: string;
  command?: string;
}

export interface Audit {
  entries?: AuditEntry[];
  attributions?: Record<string, number>;
}

// ---- Traffic ----
export interface TrafficExchange {
  id: string;
  ts: string;
  host: string;
  method: string;
  url: string;
  status: number;
  content_type: string;
  resp_len: number;
}

export interface TrafficResp {
  enabled: boolean;
  proxy?: string;
  count?: number; // global total (unfiltered)
  total?: number; // rows matching the current filter (for pagination)
  page?: number;
  size?: number;
  exchanges?: TrafficExchange[];
}

// Full raw request/response of one exchange (lazy-loaded on row select).
export interface TrafficDetail {
  req: string;
  resp: string;
}

// One distinct recorded host with its exchange count (target picker).
export interface TrafficHost {
  host: string;
  count: number;
}

// ---- App settings (runtime toggles) ----
export interface Settings {
  traffic_capture: boolean;
  agent_traffic_binding: boolean; // Agent otomatis mengikat bukti Trafik, default nonaktif; tidak memengaruhi pengikatan manual
  llm_record: boolean; // Saklar perekaman LLM (default nonaktif); saat nonaktif, tidak ada panggilan LLM yang direkam
  // Web search. brave_key_set / tavily_key_set reflect whether a key is stored
  // (the values are never returned). On PUT, send the corresponding field to set/clear.
  web_search_enabled: boolean;
  web_search_backend: string; // "ddgs" | "brave-free" | "tavily" | "deepseek"
  brave_key_set: boolean;
  tavily_key_set: boolean;
  // write-only: only sent on PUT to store/clear the key.
  brave_search_api_key?: string;
  tavily_search_api_key?: string;
  // Proxy keluar terpisah (http/https/socks5) yang dipakai untuk mengakses endpoint pencarian; tidak terkait dengan proxy MITM yang merekam Trafik. Kosong = koneksi langsung.
  web_search_proxy?: string;
  // Proxy keluar global (http/https/socks5, boleh menyertakan user:pass); seluruh Trafik target melewatinya. Saat penangkapan Trafik
  // aktif, proxy ini berfungsi sebagai upstream MITM; saat penangkapan nonaktif, proxy disuntikkan langsung ke bash/WebFetch agent. Kosong = koneksi langsung.
  global_proxy?: string;
  python_interpreter?: string; // Path interpreter python untuk Tool skrip kustom (kosong = dideteksi saat runtime)
  workers?: number; // Jumlah worker agent yang berjalan bersamaan (default 3); berlaku untuk tugas yang dimulai setelahnya
  // Batas konkurensi tugas: jumlah maksimum tugas yang "sedang berjalan" secara bersamaan. Nonaktif = tidak dibatasi; jika aktif, tugas baru yang melebihi batas akan antre dan otomatis berjalan saat ada slot kosong.
  task_concurrency_enabled?: boolean; // Default false
  task_concurrency_limit?: number; // Default 5 saat diaktifkan
  // LLM pooling (failover). Default nonaktif; jika diaktifkan, agent yang "tidak menentukan model" akan otomatis beralih ke
  // konfigurasi berikutnya saat konfigurasi saat ini tidak tersedia (saldo habis/key tidak valid/dibatasi/layanan bermasalah).
  llm_pool_enabled?: boolean; // Default false
  // Apakah agent/tugas yang terikat ke konfigurasi tertentu juga ikut jatuh kembali ke rantai pooling saat gagal. Default false = terikat berarti eksklusif.
  llm_pool_bind_fallback?: boolean;
  // Cakupan injeksi batasan operasi (default semua aktif): menyisipkan batasan allow/deny tugas ke dalam system prompt agent terkait.
  constraints_inject_planner?: boolean;
  constraints_inject_worker?: boolean;
  // Fitur eksperimental: kompresi konteks berbasis model noa (default nonaktif). Jika diaktifkan, kompresi konteks untuk empat
  // jenis agent platform (planner/worker/agent utama/percakapan) diambil alih oleh noa menggantikan compaction bawaan; dibaca sekali per run, dan
  // berlaku untuk run yang dimulai setelahnya.
  noa_compaction?: boolean;
  // ---- Notifikasi IM kerentanan (kanal itu sendiri adalah resource terpisah, lihat /api/notify/*, di sini hanya ada tiga konfigurasi global) ----
  notify_enabled?: boolean; // Saklar utama notifikasi, default aktif; dipakai untuk menghentikan sementara saat masa pemeliharaan
  notify_public_base_url?: string; // Alamat akses eksternal untuk tautan balik ke detail kerentanan; kosong = pesan tidak menyertakan tautan balik
  notify_digest_interval_min?: number; // Interval mode ringkasan (menit), default 30
}

// ---- Notifikasi IM kerentanan ----

// NotificationFilter adalah kondisi filter kanal; semua field opsional, default berarti tidak difilter.
// Backend tidak memvalidasi field apa pun: konfigurasi yang salah bentuk akan diperlakukan sebagai "cocok" (lebih baik kelebihan kirim daripada terlewat).
export interface NotificationFilter {
  min_severity?: string; // "" | low | medium | high | critical
  task_ids?: number[]; // Kosong = tidak dibatasi; jika tidak kosong, harus beririsan dengan tugas pemilik kerentanan
  asset_ids?: number[]; // Kosong = tidak dibatasi; jika tidak kosong, harus beririsan dengan aset yang ditambatkan kerentanan
  vulnclass_include?: string[]; // Kosong = terima semua; jika tidak kosong, tipe kerentanan harus cocok salah satu kata kunci (substring, tidak peka huruf besar/kecil)
  vulnclass_exclude?: string[]; // Cocok salah satu kata kunci akan dikecualikan (pengecualian lebih diprioritaskan daripada penyertaan)
  on_status_change?: boolean; // Apakah juga menerima event perubahan status penanganan kerentanan
}

// NotificationChannel adalah satu instance kanal. Field pada config berbeda-beda tergantung kind,
// dan field kredensial saat dibaca akan diganti dengan nilai mask berawalan "__masked__": mengirim balik apa adanya berarti "tidak diubah".
export interface NotificationChannel {
  id: number;
  name: string;
  kind: string;
  enabled: boolean;
  mode: "realtime" | "digest";
  config: Record<string, unknown>;
  filter: NotificationFilter;
  rate_per_min: number;
  created_at: string;
  updated_at: string;
  // secret_keys diberikan backend berdasarkan tipe kanal; frontend memakainya untuk menampilkan kotak kata sandi dan keterangan "biarkan kosong berarti tidak diubah",
  // tanpa meng-hardcode pengetahuan kanal apa pun.
  secret_keys: string[];
}

// NotificationKind adalah metadata tipe kanal yang dikembalikan oleh /api/notify/meta.
export interface NotificationKind {
  kind: string;
  default_rate_per_min: number;
  secret_keys: string[];
}

export interface NotificationMeta {
  kinds: NotificationKind[];
  enabled: boolean;
  public_base_url: string;
  digest_interval_min: string;
  defaults: { digest_interval_min: number };
  stats: {
    channels: number;
    channels_on: number;
    pending: number;
    failed: number;
    sent_today: number;
    backlog_age_ms: number;
  };
}

// NotificationDelivery adalah satu catatan pengiriman, dipakai untuk riwayat pengiriman dan pengiriman ulang saat gagal.
export interface NotificationDelivery {
  id: number;
  finding_id: string;
  event_kind: string; // finding_created | finding_status_changed
  channel_id: number;
  channel_name: string;
  channel_kind: string;
  state: "pending" | "sending" | "sent" | "failed" | "skipped";
  attempts: number;
  last_error: string;
  batch_id?: number;
  created_at: string;
  sent_at?: string;
  next_attempt_at: string;
  title: string;
  severity: string;
}

// ---- LLM config ----
export interface LLMProfile {
  id: string;
  name: string;
  format: "openai" | "anthropic" | "openai-responses";
  base_url?: string;
  proxy?: string;
  model: string;
  api_key_hint?: string;
  rate_per_second: number;
  rate_per_minute: number;
  context_window_k?: number;
  // Saklar thinking (thinking.type): "" = tidak dikirim (default) | "disabled" = nonaktif | "enabled" = aktif
  thinking_type?: string;
  // Intensitas thinking: "" = tidak dikirim (default) | "low"/"medium"/"high"/"xhigh"/"max"
  reasoning_effort?: string;
  is_default: boolean;
  // Urutan pooling: makin besar makin dulu dipilih. Konfigurasi yang aktif selalu berada di awal rantai, terlepas dari nilai ini.
  priority?: number;
  // true = tidak dijadikan target failover (tetap bisa dipakai lewat binding eksplisit oleh agent/tugas).
  pool_exclude?: boolean;
  // true (default) = streaming (SSE) | false = benar-benar non-streaming (stream:false, dikembalikan sekaligus).
  streaming?: boolean;
  // Batas maksimum output (token) untuk satu kali balasan. 0 = field ini tidak dikirim, mengikuti nilai default server.
  // Perlu dibedakan dari context_window_k: yang itu adalah kapasitas total model, hanya dipakai lokal untuk ambang kompresi.
  max_tokens?: number;
  // Nama field request yang dipakai untuk batas ini, hanya berlaku saat format="openai":
  // "" = max_tokens (default) | "max_completion_tokens" (hanya dikenali model reasoning OpenAI)
  max_tokens_field?: string;
  // Nama header sesi kustom: jika tidak kosong, setiap request menyertakan header HTTP ini dengan nilai = session id dari sesi/intent saat ini.
  // "" = tidak dikirim. Dipakai untuk gateway yang melakukan prompt caching/sticky routing berdasarkan header session-id.
  session_header_key?: string;
  // Override retry untuk konfigurasi ini (koneksi/respons kosong/jendela aman provider yang sama). Kosong/semua 0 = mengikuti kebijakan global.
  retry?: LLMRetryOverride;
}

// ---- Kebijakan Retry LLM ----
// Dua pengaturan untuk satu lapisan retry. Keduanya berarti "0 = belum dikonfigurasi":
//   attempts    0 = pakai jumlah default | -1 = nonaktifkan retry lapisan ini | >0 = jumlah retry
//   interval_ms 0 = pakai exponential backoff default | >0 = pakai interval milidetik tetap ini
export interface LLMRetryRule {
  attempts: number;
  interval_ms: number;
}

// Tiga lapisan yang bisa di-override oleh satu konfigurasi LLM (semuanya retry yang "mengikuti endpoint").
export interface LLMRetryOverride {
  connect: LLMRetryRule; // Retry koneksi: connection reset/timeout/429/5xx, sebelum stream dimulai
  empty: LLMRetryRule; // Retry respons kosong: selesai tapi tanpa konten apa pun (hanya format openai)
  stream: LLMRetryRule; // Retry jendela aman provider yang sama: replay ulang saat stream terputus sebelum output terkirim
}

// Kebijakan global = nilai default tiga lapisan di atas, ditambah dua lapisan yang hanya ada secara global:
//   breaker circuit breaker pooling (attempts = berapa kali kegagalan sesaat berturut-turut memicu breaker, interval_ms = durasi cooldown tetap)
//   intent  rerun intent (seluruh intent dijalankan ulang setelah worker berakhir dengan model_error)
export interface LLMRetryPolicy extends LLMRetryOverride {
  breaker: LLMRetryRule;
  intent: LLMRetryRule;
}

// ---- LLM Pooling (Failover) ----
// Posisi dan status kesehatan satu konfigurasi dalam rantai pooling. state:
//   ok       normal
//   degraded ada kegagalan berturut-turut tapi belum mencapai ambang breaker
//   tripped  sudah terpicu breaker, dilewati selama masa cooldown (cooldown_secs adalah sisa detiknya)
export interface LLMPoolMember {
  profile_id: string;
  name: string;
  model: string;
  format: string;
  priority: number;
  active: boolean; // Apakah ini konfigurasi yang sedang aktif (selalu berada di awal rantai)
  excluded: boolean; // pool_exclude: tidak ikut dalam pooling
  state: "ok" | "degraded" | "tripped";
  fails: number;
  trips: number;
  cooldown_secs: number;
  last_error?: string;
  last_at?: string;
}

export interface LLMPoolStatus {
  enabled: boolean;
  bind_fallback: boolean;
  chain: LLMPoolMember[];
}

// ---- Agents ----
export interface Agent {
  id: string;
  key: string; // Bawaan berupa goals/planner/mainagent/worker; kustom berarti key yang ditentukan pengguna sendiri
  name: string;
  description?: string;
  role: string;
  builtin: boolean;
  enabled: boolean;
  llm_profile_id?: number | null; // Konfigurasi LLM yang diikat; null/absent = mengikuti tugas/sesi/global
  max_turns?: number; // 0 = tidak dibatasi
  run_seconds?: number; // Batas waktu nyata (wall clock) satu kali run worker (detik); 0 = tidak dibatasi
  web_search?: boolean; // Apakah pencarian web diaktifkan (dikontrol oleh saklar global sistem)
  interactive_shell?: boolean; // Apakah interactive shell diaktifkan (kelompok Tool sesi PTY yang persisten)
  // Strategi penanganan setelah trigger P3 (hanya relevan untuk agent kustom)
  trigger_run_mode?: "serial" | "parallel"; // Antre serial / setiap trigger menjalankan sesi secara paralel masing-masing
  trigger_merge_mode?: "by_task" | "all" | "none"; // Hanya untuk serial: gabung tugas yang sama / gabung semua / tidak digabung
  trigger_max_parallel?: number; // Hanya untuk parallel: batas konkurensi per agent; 0 = tidak dibatasi
  // Jumlah binding (hanya dikembalikan oleh endpoint list): MCP yang terlihat / Skill yang terlihat / Tool yang terikat
  mcp_count?: number;
  skill_count?: number;
  tool_count?: number;
}

export interface PromptVar {
  name: string;
  description: string;
  example: string;
  source: "exploration" | "runtime" | "distilled";
}

export interface PromptVersion {
  version: number;
  ts: string;
  note: string;
  template_text: string;
}

export interface AgentDetail {
  agent: Agent;
  prompt: string;
  variables: PromptVar[];
  versions: PromptVersion[];
  visibility: { mcp: number[]; skill: string[] };
  // Kandidat konfigurasi LLM yang bisa diikat (untuk dropdown "model default"); binding saat ini lihat agent.llm_profile_id
  llm_profiles?: { id: number; name: string; model: string; is_default: boolean }[];
  wrapup_prompt?: string; // Prompt penutup yang tersimpan (kosong = pakai default bawaan)
  wrapup_default?: string; // Prompt penutup default bawaan (placeholder/pemulihan default)
  wrapup_max_turns?: number; // Jumlah giliran penutup yang tersimpan (0 = pakai default bawaan)
  wrapup_max_turns_default?: number; // Jumlah giliran penutup default bawaan (untuk keterangan "0 = default N")
  // Kata penutup timeout tingkat tugas (hanya worker/planner, bagian ini hanya ditampilkan saat task_timeout_wrapup_supported=true)
  task_timeout_wrapup_supported?: boolean;
  task_timeout_wrapup_prompt?: string;
  task_timeout_wrapup_default?: string;
  task_timeout_wrapup_max_turns?: number;
  task_timeout_wrapup_max_turns_default?: number;
}

// ---- MCP ----
export interface MCPServer {
  id: number;
  name: string;
  transport: "stdio" | "http" | "sse";
  command?: string;
  args: string[];
  env: Record<string, string>;
  url?: string;
  enabled: boolean;
  insecure?: boolean; // http: skip TLS cert verification (self-signed servers)
  tools?: string[]; // mcp_tools_cache (names only, for the count)
}

export interface MCPTool {
  name: string;
  description: string;
}

// ---- Skills ----
// Fields align with the agentskills.io open specification.
// description covers both "what the skill does" and "when to use it".
export interface SkillItem {
  name: string; // unique key = directory name
  description?: string; // required per spec; covers what + when to use
  license?: string; // optional: SPDX identifier or free text
  compatibility?: string; // optional: environment requirements
  mcps?: string[]; // MCP server names this skill unlocks on load
  files: string[]; // files in the skill directory
  // Statistik pemanggilan (buku besar skill_usage). Skill yang belum pernah dipanggil: calls=0, last_used tidak ada.
  calls: number;
  tasks: number; // Jumlah tugas yang pernah memuatnya (sesi chat tidak dihitung)
  usage_agents: string[]; // Key agent yang pernah memuatnya
  last_used?: string;
}

// SkillCall adalah satu kali pemanggilan Skill() (daftar pemanggilan terbaru untuk satu skill).
export interface SkillCall {
  ts: string;
  agent_key: string;
  task_id: number; // 0 = bukan skenario tugas (sesi percakapan)
  session_id: string;
  args_len: number;
}

// MissingSkill adalah skill yang disebut namanya tapi tidak ada, celah "ingin dipakai tapi tidak tersedia".
export interface MissingSkill {
  skill: string;
  calls: number;
  agents: string[];
  last_used?: string;
}

// ---- Tools (katalog Tool bawaan) ----
// key + handler live in Go; only these fields are page-editable. system tools lock
// the key and the parameter *structure* (name/type/required) — the per-param
// description/default and the agent binding are what move.
export interface Tool {
  key: string;
  system: boolean;
  description: string;
  // eslint-disable-next-line @typescript-eslint/no-explicit-any
  schema: Record<string, any>; // full JSON-Schema (object with properties)
  agents: string[]; // bound agent keys
  enabled: boolean;
  kind?: "builtin" | "shell" | "command" | "script" | "http"; // Tipe Tool kustom
  // eslint-disable-next-line @typescript-eslint/no-explicit-any
  exec?: Record<string, any>; // Spesifikasi eksekusi Tool kustom (kind!=builtin)
  deferred?: boolean; // Schema ditunda (SearchExtraTools/ExecuteExtraTool)
  calls?: number; // persistent runtime invocation count (older APIs may omit it)
}

// ---- Stats ----
export interface Stats {
  assets: number;
  engine_mode: EngineMode;
  llm_configured: boolean;
  roe_enabled: boolean;
  findings_confirmed: number;
  active_task?: Partial<Task>;
}

// ---- Intercept Rules ----
export type InterceptAction = "allow" | "deny" | "ask";
export type InterceptMatchTarget = "tool_name" | "tool_input";
export type InterceptMatchType = "string" | "regex";

export interface InterceptRule {
  id: number;
  name: string;
  enabled: boolean;
  priority: number;
  match_target: InterceptMatchTarget;
  match_type: InterceptMatchType;
  pattern: string;
  action: InterceptAction;
  message: string;
  timeout_enabled: boolean;
  timeout_seconds: number;
  timeout_action: "deny" | "allow";
  created_at: string;
  updated_at: string;
}

// ---- Asset Intercept Rules (Intersepsi Aset: daftar hitam global) ----
export type AssetInterceptKind =
  | "exact_domain"
  | "exact_ip"
  | "exact_url"
  | "fuzzy_domain"
  | "fuzzy_ip"
  | "fuzzy_url"
  | "cidr";

// action hanya dipakai untuk aturan tingkat tugas: block = Intersepsi (pengujian dilarang), allow = izinkan (whitelist).
export type AssetInterceptAction = "block" | "allow";

// Item input untuk aturan Intersepsi/izin aset tingkat tugas (dipakai saat membuat tugas dan mengedit detail tugas).
export interface AssetInterceptRuleInput {
  action: AssetInterceptAction;
  kind: AssetInterceptKind;
  pattern: string;
  note: string;
  enabled: boolean;
}

export interface AssetInterceptRule {
  id: number;
  enabled: boolean;
  action?: AssetInterceptAction; // Aturan global tidak memiliki field ini (selalu berarti Intersepsi); aturan tingkat tugas membedakan block/allow
  kind: AssetInterceptKind;
  pattern: string;
  note: string;
  builtin: boolean;
  created_at: string;
  updated_at: string;
}

export interface InterceptPending {
  decision_source?: "rule" | "model" | "unknown" | "";
  id: number;
  rule_id?: number;
  conversation_id?: number;
  task_id?: string;
  agent_name: string;
  tool_name: string;
  // eslint-disable-next-line @typescript-eslint/no-explicit-any
  tool_input: Record<string, any>;
  status: "pending" | "allowed" | "denied" | "timeout";
  reason: string; // Message aturan atau alasan keputusan model (keputusan model diberi awalan [model])
  decided_at?: string;
  created_at: string;
}

// JudgeConfig: konfigurasi global untuk Persetujuan cadangan oleh model (model memutuskan hanya saat tidak ada aturan Intersepsi yang cocok).
export interface JudgeConfig {
  enabled: boolean;
  profile_id: number; // 0 = mengikuti konfigurasi aktif/default
  prompt: string; // Prompt keputusan; jika belum diatur, GET akan diisi backend dengan teks lengkap template bawaan
  timeout_seconds: number; // Timeout pemanggilan model
  fail_action: "allow" | "ask" | "deny"; // Fallback saat model error/timeout/tidak bisa diparsing
  ask_timeout_seconds: number; // Timeout menunggu Persetujuan setelah model memutuskan ask dan dialihkan ke manusia
  ask_timeout_action: "allow" | "deny"; // Aksi default setelah Persetujuan timeout
}

// JudgeUsage: total penggunaan token Persetujuan cadangan model (kanal judge) ditambah seri harian N hari terakhir.
export interface JudgeDayUsage {
  date: string; // YYYY-MM-DD (UTC)
  calls: number;
  input_tokens: number;
  output_tokens: number;
}
export interface JudgeUsage {
  calls: number;
  input_tokens: number;
  output_tokens: number;
  cache_read_tokens: number;
  cache_write_tokens: number;
  daily: JudgeDayUsage[];
}

export interface InterceptApprovalFilter {
  status?: InterceptPending["status"];
  decision_source?: "rule" | "model" | "unknown";
}

// InterceptApprovalRow enriches InterceptPending with conversation/task and rule context.
export interface InterceptApprovalRow extends InterceptPending {
  conv_title: string; // "" if no linked conversation
  conv_agent_key: string; // "" if no linked conversation
  rule_name: string; // "" if rule was deleted
}

// ── Sinkronisasi Aset (sumber data ScopeSentry) ─────────────────────────────────────────────────────────────
export interface SSProject {
  id: string; // MongoDB ObjectID — used as filter.project
  name: string;
  logo?: string;
  AssetCount?: number;
  tag?: string;
}

export interface SSTask {
  id: string;
  name: string; // used as filter.task
  status?: number;
  progress?: number;
  creatTime?: string;
  endTime?: string;
}

// ConvTokenSummary — one conversation's token total (+ profile/date) for merging
// chat usage into the dashboard token stats. GET /api/tokens/conversations.
export interface ConvTokenSummary {
  llm_profile_id: number | null;
  created_at: string;
  input_tokens: number;
  output_tokens: number;
  cache_read_tokens: number;
  cache_write_tokens: number;
}

// ---- Command recording (Bash execution history) ----
export interface CommandRecord {
  id: number;
  exploration_id: number;
  worker: string;
  tool: string;
  command: string; // raw tool input (JSON)
  output: string;
  is_error: boolean;
  created_at: string;
}

// Statistik pemanggilan satu Tool (/commands/stats); errors adalah jumlah yang gagal di antaranya.
export interface ToolStat {
  tool: string;
  total: number;
  errors: number;
}

// ---- LLM recording ----
export interface LLMRecordItem {
  id: number;
  ts: string;
  model: string;
  profile_name: string;
  session_id: string;
  task_id: string;
  worker: string;
  latency_ms: number;
  input_tokens: number;
  output_tokens: number;
  cache_read: number;
  cache_write: number;
  status: string;
  error?: string;
}

export interface LLMRecordDetail extends LLMRecordItem {
  request_body: string;
  response_body: string;
  // Teks HTTP asli yang benar-benar dikirim/diterima provider: request berupa body lengkap yang dikirim buildBody() (termasuk schema Tool),
  // response berupa frame SSE mentah. request_body/response_body di atas adalah tampilan yang dinormalisasi,
  // membuang schema Tool dan blok tool_use. Kosong untuk catatan lama.
  raw_request?: string;
  raw_response?: string;
}

// One distinct task with its LLM-record count (task picker on the records page).
export interface LLMTask {
  task_id: string;
  count: number;
}

// The exact JSON sent to the review model, retained for all model verdicts.
export interface InterceptReviewInput {
  version: number;
  background?: {
    // worker_summary is retained only for immutable v2/v3 snapshots.
    source: "user_message" | "worker_summary";
    text: string;
    truncated?: boolean;
  };
  // Version 1 snapshots are immutable and remain readable in historical audits.
  task?: {
    task_id: number;
    description: string;
    goal: string;
    constraints: { id: number; kind: string; text: string; origin: string; created_at: number }[];
    truncated?: boolean;
  };
  working_directory?: string;
  worker_intent?: string;
  turn_input?: string;
  background_truncated?: boolean;
  // Legacy v1/v2 snapshots only; v3 never sends execution history.
  history?: {
    tool_use_id: string;
    tool: string;
    arguments_preview: string;
    result: string;
    status: "succeeded" | "failed";
    truncated?: boolean;
  }[];
  history_truncated?: boolean;
  correlation?: "exact" | "ambiguous" | "unavailable";
  tool_name: string;
  arguments: Record<string, unknown>;
}

// Immutable review snapshot plus separately recorded execution outcome.
export interface InterceptAudit {
  model_input?: InterceptReviewInput;
  model_input_digest?: string;
  run_id?: string;
  tool_use_id?: string;
  correlation: "exact" | "ambiguous" | "unavailable";
  input_digest: string;
  user_message: string;
  user_truncated?: boolean;
  context:
    | { kind: string; tool?: string; tool_use_id?: string; text: string; is_error?: boolean; truncated?: boolean }[]
    | null;
  context_truncated?: boolean;
  captured_at: string;
  model_fallback?: boolean;
  initial_action: "allow" | "ask" | "deny";
  initial_reason: string;
  effective_action?: "allow" | "deny";
  decision_reason?: string;
  rule_name?: string;
  config_digest?: string;
  profile_id?: number;
  execution_status: "not_started" | "not_executed" | "awaiting_result" | "succeeded" | "failed" | "unknown";
  output?: string;
  output_truncated?: boolean;
  execution_ended_at?: string;
}
export interface InterceptDetail extends InterceptApprovalRow {
  audit: InterceptAudit | null;
}

export type TrafficEvidenceRole = "baseline" | "proof" | "verification" | "supporting";
export interface TrafficEvidenceRef {
  traffic_id: string;
  role?: TrafficEvidenceRole;
  note?: string;
}
export interface TrafficEvidenceSnapshot {
  id: string;
  source_traffic_id: string;
  captured_at: number;
  url: string;
  method: string;
  status: number;
  content_type: string;
  req_head?: string;
  resp_head?: string;
  req_hash: string;
  resp_hash: string;
  req_len: number;
  resp_len: number;
}
export interface FindingTrafficBinding {
  id: string;
  finding_id: string;
  snapshot_id: string;
  role: TrafficEvidenceRole;
  note: string;
  position: number;
  created_at: string;
  snapshot: TrafficEvidenceSnapshot;
}
export interface FindingTraffic {
  finding_id: string;
  version: number;
  report_version: number;
  bindings: FindingTrafficBinding[];
}
export interface EvidenceBodyPreview {
  content: string;
  offset: number;
  total: number;
  next_offset: number;
  truncated: boolean;
  binary: boolean;
}
export interface FindingTrafficDetail {
  binding: FindingTrafficBinding;
  request: EvidenceBodyPreview;
  response: EvidenceBodyPreview;
}

/** GET /api/update/check, hasil perbandingan antara versi saat ini dengan rilis resmi terbaru di GitHub. */
export interface UpdateCheck {
  /** Versi yang sedang berjalan; untuk build pengembangan berupa "dev" atau bentuk dengan akhiran dari git describe. */
  current: string;
  /** Mode menjalankan. Di docker, penggantian versi hanya berlaku pada writable layer kontainer; membangun ulang kontainer akan kembali ke versi image. */
  mode: "docker" | "binary";
  os: string;
  arch: string;
  repo: string;
  /** Apakah ada versi sebelumnya yang bisa dikembalikan (artex.old). */
  has_backup: boolean;
  /** Kesimpulan proses bootstrap pembaruan otomatis saat startup ini (penggantian versi gagal / sudah dikembalikan, dll), kosong jika tidak ada kejadian. */
  boot_notice?: string;
  rolled_back?: boolean;
  /** Alasan yang diberikan saat query ke GitHub gagal; jika ini terjadi, field-field di bawah tidak akan ada. */
  error?: string;
  latest?: string;
  notes?: string;
  html_url?: string;
  published_at?: string;
  /** Nama paket rilis untuk platform saat ini, beserta apakah Release tersebut benar-benar menyertakannya. */
  asset?: string;
  asset_available?: boolean;
  size?: number;
  has_update?: boolean;
  /** Apakah kedua nomor versi bisa dibandingkan; untuk build pengembangan bernilai false, dan pembaruan sekali klik dinonaktifkan dalam kondisi ini. */
  comparable?: boolean;
  /** Keterangan saat comparable bernilai false. */
  reason?: string;
}

/** Satu progres pembaruan yang dikirim oleh /api/update/stream. */
export interface UpdateProgress {
  phase: "idle" | "downloading" | "verifying" | "extracting" | "staged" | "failed";
  /** Hanya berarti pada tahap unduhan (0-100); tahap lainnya bernilai -1. */
  percent: number;
  message: string;
  version?: string;
  error?: string;
}

// Original execution selected from an approval, never submitted to the reviewer.
export interface InterceptExecution {
  conversation_id: number | null;
  task_id: string | null;
  session: string;
  seq: number;
  items: Activity[];
}
