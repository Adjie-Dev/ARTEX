"use client";

import * as React from "react";
import { toast } from "sonner";
import {
  BotIcon,
  ListFilterIcon,
  PencilIcon,
  PlusIcon,
  ShieldAlertIcon,
  Trash2Icon,
} from "lucide-react";

import { Badge } from "@/components/ui/badge";
import { Button } from "@/components/ui/button";
import {
  Dialog,
  DialogContent,
  DialogDescription,
  DialogHeader,
  DialogTitle,
} from "@/components/ui/dialog";
import { Input } from "@/components/ui/input";
import { Label } from "@/components/ui/label";
import { Separator } from "@/components/ui/separator";
import { Switch } from "@/components/ui/switch";
import { Textarea } from "@/components/ui/textarea";
import {
  Select,
  SelectContent,
  SelectItem,
  SelectTrigger,
  SelectValue,
} from "@/components/ui/select";
import {
  Sheet,
  SheetContent,
  SheetDescription,
  SheetFooter,
  SheetHeader,
  SheetTitle,
} from "@/components/ui/sheet";
import {
  Table,
  TableBody,
  TableCell,
  TableHead,
  TableHeader,
  TableRow,
} from "@/components/ui/table";
import { Card, CardContent } from "@/components/ui/card";
import { Tabs, TabsContent, TabsList, TabsTrigger } from "@/components/ui/tabs";
import { api } from "@/lib/api";
import type {
  InterceptAction,
  InterceptRule,
  JudgeConfig,
  JudgeDayUsage,
  JudgeUsage,
  LLMProfile,
  Tool,
} from "@/lib/types";

// fmtTokens memadatkan jumlah token menjadi notasi singkat (1.2k / 3.4M), dipakai untuk statistik penggunaan persetujuan.
function fmtTokens(n: number): string {
  if (n >= 1_000_000) return (n / 1_000_000).toFixed(1) + "M";
  if (n >= 1000) return (n / 1000).toFixed(1) + "k";
  return String(n);
}

// JudgeStat adalah satu blok angka statistik (label + nilai).
function JudgeStat({ label, value }: { label: string; value: string }) {
  return (
    <div className="rounded-lg border bg-muted/20 px-3 py-2">
      <div className="text-[10px] text-muted-foreground">{label}</div>
      <div className="mt-0.5 text-lg font-semibold tabular-nums">{value}</div>
    </div>
  );
}

// JudgeSparkbars menggambar mini bar chart konsumsi harian N hari terakhir (input+output) hanya dengan div, tanpa dependensi library chart.
function JudgeSparkbars({ daily }: { daily: JudgeDayUsage[] }) {
  const max = Math.max(1, ...daily.map((d) => d.input_tokens + d.output_tokens));
  return (
    <div className="flex h-16 items-end gap-0.5">
      {daily.map((d) => {
        const total = d.input_tokens + d.output_tokens;
        const h = Math.max(2, Math.round((total / max) * 100));
        return (
          <div
            key={d.date}
            title={`${d.date} · ${d.calls} kali · ${fmtTokens(total)} tokens`}
            className="min-w-[2px] flex-1 rounded-sm bg-violet-500/60 hover:bg-violet-500"
            style={{ height: `${h}%` }}
          />
        );
      })}
    </div>
  );
}

// ---- tool scope ----

// SDK tools are intentionally not seeded into the DB (they apply to every agent
// and have no per-agent binding). We hardcode them here so they still appear in
// the scope dialog.
function sdkTool(key: string, description: string): Tool {
  return { key, system: true, description, schema: {}, agents: [], enabled: true, kind: "builtin" };
}

const SDK_EXEC: Tool[] = [
  sdkTool("Bash",        "Menjalankan command di shell"),
  sdkTool("WebFetch",    "Mengirim request HTTP/HTTPS (termasuk dukungan proxy)"),
  sdkTool("web_search",  "Pencarian web"),
  sdkTool("shell_open",  "Membuka sesi interaktif PTY yang persisten"),
  sdkTool("shell_send",  "Mengirim input ke sesi interaktif"),
  sdkTool("shell_read",  "Membaca output sesi interaktif"),
  sdkTool("shell_close", "Menutup sesi interaktif"),
  sdkTool("shell_list",  "Menampilkan semua sesi interaktif"),
];

const SDK_WRITE: Tool[] = [
  sdkTool("Write",     "Menulis file"),
  sdkTool("Edit",      "Mengedit file (penggantian presisi)"),
  sdkTool("MultiEdit", "Mengedit banyak file sekaligus"),
];

const SDK_KEYS = new Set([...SDK_EXEC, ...SDK_WRITE].map((t) => t.key));

function groupTools(dbTools: Tool[]) {
  const sys: Tool[] = [], custom: Tool[] = [];
  for (const t of dbTools) {
    if (SDK_KEYS.has(t.key)) continue; // already covered by hardcoded groups
    if (t.system) sys.push(t);
    else          custom.push(t);
  }
  return [
    { label: "Eksekusi",      tools: SDK_EXEC },
    { label: "Tulis/Edit", tools: SDK_WRITE },
    { label: "Tool Sistem",    tools: sys },
    { label: "Tool Kustom",  tools: custom },
  ].filter((g) => g.tools.length > 0);
}

// ---- form state ----

type RuleForm = {
  name: string;
  enabled: boolean;
  priority: number;
  match_target: "tool_name" | "tool_input";
  match_type: "string" | "regex";
  pattern: string;
  action: InterceptAction;
  message: string;
  timeout_enabled: boolean;
  timeout_seconds: number;
  timeout_action: "deny" | "allow";
};

const defaultForm = (): RuleForm => ({
  name: "",
  enabled: true,
  priority: 0,
  match_target: "tool_name",
  match_type: "string",
  pattern: "",
  action: "deny",
  message: "",
  timeout_enabled: true,
  timeout_seconds: 60,
  timeout_action: "deny",
});

// ---- small components ----

function ActionBadge({ action }: { action: InterceptAction }) {
  if (action === "allow") return <Badge variant="secondary">Izinkan</Badge>;
  if (action === "deny")  return <Badge variant="destructive">Larang</Badge>;
  return <Badge variant="outline" className="border-amber-400 text-amber-600">Ajukan</Badge>;
}

function Field({ label, children }: { label: string; children: React.ReactNode }) {
  return (
    <div className="space-y-1.5">
      <Label className="text-xs font-medium text-muted-foreground uppercase tracking-wide">
        {label}
      </Label>
      {children}
    </div>
  );
}

// ---- LLM fallback judge card ----

const FOLLOW_ACTIVE = "0"; // profile_id 0 = mengikuti konfigurasi aktif/default

const defaultJudge = (): JudgeConfig => ({
  enabled: false,
  profile_id: 0,
  prompt: "",
  timeout_seconds: 15,
  fail_action: "allow",
  ask_timeout_seconds: 300,
  ask_timeout_action: "deny",
});

function JudgeCard() {
  const [cfg, setCfg] = React.useState<JudgeConfig>(defaultJudge());
  const [profiles, setProfiles] = React.useState<LLMProfile[]>([]);
  const [loading, setLoading] = React.useState(true);
  const [saving, setSaving] = React.useState(false);
  const [usage, setUsage] = React.useState<JudgeUsage | null>(null);

  // Statistik penggunaan persetujuan: kegagalan tidak mengganggu halaman konfigurasi, hanya diambil saat diaktifkan.
  const loadUsage = React.useCallback(async () => {
    try {
      setUsage(await api.interceptJudgeUsage(30));
    } catch {
      // Diabaikan: statistik yang tidak tersedia seharusnya tidak memengaruhi pengeditan konfigurasi
    }
  }, []);

  const load = React.useCallback(async () => {
    setLoading(true);
    try {
      const [j, ps] = await Promise.all([api.interceptGetJudgeConfig(), api.llmProfiles()]);
      setCfg(j);
      setProfiles(ps);
    } catch (e) {
      toast.error("Gagal memuat konfigurasi persetujuan cadangan model: " + (e as Error).message);
    } finally {
      setLoading(false);
    }
  }, []);

  React.useEffect(() => {
    load();
  }, [load]);

  // Mengambil statistik penggunaan persetujuan setelah diaktifkan (termasuk saat pemuatan awal membaca switch sebagai true).
  React.useEffect(() => {
    if (cfg.enabled) loadUsage();
  }, [cfg.enabled, loadUsage]);

  function patch(p: Partial<JudgeConfig>) {
    setCfg((c) => ({ ...c, ...p }));
  }

  async function save() {
    setSaving(true);
    try {
      await api.interceptSetJudgeConfig(cfg);
      toast.success("Konfigurasi persetujuan cadangan model berhasil disimpan");
      await load(); // Membaca ulang: jika prompt dikosongkan, akan diisi kembali dengan template bawaan
    } catch (e) {
      toast.error("Gagal menyimpan: " + (e as Error).message);
    } finally {
      setSaving(false);
    }
  }

  async function restorePrompt() {
    // Mengosongkan prompt lalu menyimpan → server akan mengembalikan teks lengkap template bawaan pada permintaan berikutnya, lalu mengisi kembali input.
    setSaving(true);
    try {
      await api.interceptSetJudgeConfig({ ...cfg, prompt: "" });
      const j = await api.interceptGetJudgeConfig();
      setCfg(j);
      toast.success("Template default bawaan berhasil dipulihkan");
    } catch (e) {
      toast.error("Gagal memulihkan: " + (e as Error).message);
    } finally {
      setSaving(false);
    }
  }

  return (
    <div className="space-y-4">
      {/* Switch aktivasi, baris sorotan tersendiri */}
      <div
        className={`flex items-center justify-between gap-3 rounded-lg border px-4 py-3 ${
          cfg.enabled ? "border-violet-400/50 bg-violet-50/40 dark:bg-violet-950/20" : "bg-muted/40"
        }`}
      >
        <div className="flex items-center gap-2.5">
          <BotIcon className={`h-5 w-5 shrink-0 ${cfg.enabled ? "text-violet-600" : "text-muted-foreground"}`} />
          <div>
            <p className="text-sm font-semibold leading-tight">Persetujuan Cadangan oleh Model</p>
            <p className="text-xs text-muted-foreground mt-0.5">
              Hanya command yang berada dalam <span className="font-medium text-foreground">cakupan intersepsi</span> dan <span className="font-medium text-foreground">tidak cocok dengan aturan intersepsi apa pun</span> yang akan dinilai secara semantik oleh model (izinkan / alihkan ke manusia / intersepsi)
            </p>
          </div>
        </div>
        <div className="flex shrink-0 items-center gap-2">
          <span className="text-xs text-muted-foreground">{cfg.enabled ? "Aktif" : "Tidak aktif"}</span>
          <Switch checked={cfg.enabled} disabled={loading} onCheckedChange={(v) => patch({ enabled: v })} />
        </div>
      </div>

      {/* Statistik penggunaan token persetujuan (akumulasi global, terpisah dari masing-masing konfigurasi model) */}
      {cfg.enabled && usage && (
        <Card>
          <CardContent className="p-4">
            <div className="mb-3 flex items-center justify-between">
              <div>
                <p className="text-sm font-medium">Konsumsi Token Persetujuan</p>
                <p className="text-xs text-muted-foreground">
                  Akumulasi penggunaan persetujuan cadangan model, dihitung terpisah (worker=judge), tidak tercampur dengan statistik masing-masing konfigurasi model
                </p>
              </div>
              <Button variant="ghost" size="sm" className="h-7 text-xs" onClick={loadUsage}>
                Muat ulang
              </Button>
            </div>
            <div className="grid grid-cols-2 gap-3 sm:grid-cols-5">
              <JudgeStat label="Panggilan Persetujuan" value={usage.calls.toLocaleString()} />
              <JudgeStat label="Token Input" value={fmtTokens(usage.input_tokens)} />
              <JudgeStat label="Token Output" value={fmtTokens(usage.output_tokens)} />
              <JudgeStat label="Baca Cache" value={fmtTokens(usage.cache_read_tokens)} />
              <JudgeStat label="Tulis Cache" value={fmtTokens(usage.cache_write_tokens)} />
            </div>
            {usage.daily.length > 0 && (
              <div className="mt-4">
                <p className="mb-2 text-[10px] uppercase tracking-wider text-muted-foreground">
                  Konsumsi harian 30 hari terakhir (input + output)
                </p>
                <JudgeSparkbars daily={usage.daily} />
              </div>
            )}
          </CardContent>
        </Card>
      )}

      {cfg.enabled && (
        <div className="grid gap-4 lg:grid-cols-5">
          {/* Kiri: editor prompt (ditampilkan langsung, area utama) */}
          <Card className="lg:col-span-3">
            <CardContent className="flex h-full flex-col gap-2 p-4">
              <div className="flex items-center justify-between">
                <div>
                  <p className="text-sm font-medium">Prompt Persetujuan</p>
                  <p className="text-xs text-muted-foreground">Model menentukan ALLOW / ASK / DENY berdasarkan ini, bisa diedit langsung</p>
                </div>
                <Button variant="ghost" size="sm" className="h-7 text-xs" onClick={restorePrompt} disabled={saving}>
                  Pulihkan template default
                </Button>
              </div>
              <Textarea
                className="min-h-[22rem] flex-1 resize-none font-mono text-xs leading-relaxed"
                value={cfg.prompt}
                onChange={(e) => patch({ prompt: e.target.value })}
                placeholder="Dikosongkan untuk memakai template bawaan"
                spellCheck={false}
              />
              <p className="text-right text-[11px] text-muted-foreground">{cfg.prompt.length} karakter</p>
            </CardContent>
          </Card>

          {/* Kanan: parameter penentuan (panel setelan) */}
          <Card className="lg:col-span-2">
            <CardContent className="space-y-5 p-4">
              <div className="space-y-4">
                <p className="text-[10px] font-semibold uppercase tracking-wider text-muted-foreground">Model dan Kebijakan Penentuan</p>
                <Field label="Model Persetujuan">
                  <Select value={String(cfg.profile_id || 0)} onValueChange={(v) => patch({ profile_id: Number(v) })}>
                    <SelectTrigger><SelectValue /></SelectTrigger>
                    <SelectContent>
                      <SelectItem value={FOLLOW_ACTIVE}>Mengikuti konfigurasi aktif</SelectItem>
                      {profiles.map((p) => (
                        <SelectItem key={p.id} value={p.id}>
                          {p.name}（{p.model}）
                        </SelectItem>
                      ))}
                    </SelectContent>
                  </Select>
                </Field>
                <Field label="Timeout Penentuan Model (detik)">
                  <Input
                    type="number"
                    min={1}
                    value={cfg.timeout_seconds}
                    onChange={(e) => {
                      const n = parseInt(e.target.value, 10);
                      if (n > 0) patch({ timeout_seconds: n });
                    }}
                  />
                </Field>
                <Field label="Jika Model Gagal (error / timeout / tidak bisa diparse)">
                  <Select value={cfg.fail_action} onValueChange={(v) => patch({ fail_action: v as JudgeConfig["fail_action"] })}>
                    <SelectTrigger><SelectValue /></SelectTrigger>
                    <SelectContent>
                      <SelectItem value="allow">Izinkan</SelectItem>
                      <SelectItem value="ask">Alihkan ke Persetujuan Manusia</SelectItem>
                      <SelectItem value="deny">Intersepsi</SelectItem>
                    </SelectContent>
                  </Select>
                </Field>
              </div>

              <Separator />

              <div className="space-y-4">
                <p className="text-[10px] font-semibold uppercase tracking-wider text-muted-foreground">Persetujuan Manusia (saat model menilai "alihkan ke manusia")</p>
                <Field label="Timeout Menunggu Persetujuan (detik)">
                  <Input
                    type="number"
                    min={5}
                    value={cfg.ask_timeout_seconds}
                    onChange={(e) => {
                      const n = parseInt(e.target.value, 10);
                      if (n > 0) patch({ ask_timeout_seconds: n });
                    }}
                  />
                </Field>
                <Field label="Aksi Default Setelah Timeout">
                  <Select value={cfg.ask_timeout_action} onValueChange={(v) => patch({ ask_timeout_action: v as JudgeConfig["ask_timeout_action"] })}>
                    <SelectTrigger><SelectValue /></SelectTrigger>
                    <SelectContent>
                      <SelectItem value="deny">Intersepsi</SelectItem>
                      <SelectItem value="allow">Izinkan</SelectItem>
                    </SelectContent>
                  </Select>
                </Field>
              </div>
            </CardContent>
          </Card>
        </div>
      )}

      <div className="flex justify-end">
        <Button size="sm" onClick={save} disabled={saving || loading}>
          {saving ? "Menyimpan..." : "Simpan Konfigurasi"}
        </Button>
      </div>
    </div>
  );
}

// ---- page ----

export default function InterceptPage() {
  const [rules, setRules]     = React.useState<InterceptRule[]>([]);
  const [loading, setLoading] = React.useState(true);
  const [open, setOpen]       = React.useState(false);
  const [editing, setEditing] = React.useState<InterceptRule | null>(null);
  const [form, setForm]       = React.useState<RuleForm>(defaultForm());
  const [saving, setSaving]   = React.useState(false);
  const [regexErr, setRegexErr] = React.useState("");
  const [regexWarn, setRegexWarn] = React.useState(false); // true = JS tidak bisa mem-parsing tapi kemungkinan merupakan sintaks Go yang valid

  // ---- tool scope dialog ----
  const [scopeOpen, setScopeOpen]       = React.useState(false);
  const [allTools, setAllTools]         = React.useState<Tool[]>([]);
  const [enabledTools, setEnabledTools] = React.useState<Set<string>>(new Set());
  const [scopeLoading, setScopeLoading] = React.useState(false);
  const [scopeSaving, setScopeSaving]   = React.useState(false);
  const [scopeTools, setScopeTools]     = React.useState<string[]>([]); // Bar informasi di header: tool yang saat ini masuk ke dalam intersepsi

  // ---- data ----

  const loadScope = React.useCallback(async () => {
    try {
      const cfg = await api.interceptGetToolConfig();
      setScopeTools(cfg.enabled_tools);
    } catch {
      // Bar informasi tidak krusial, gagal secara senyap
    }
  }, []);

  const load = React.useCallback(async () => {
    try {
      const r = await api.interceptRules();
      setRules(r);
    } catch {
      toast.error("Gagal memuat aturan intersepsi");
    } finally {
      setLoading(false);
    }
  }, []);

  React.useEffect(() => { load(); loadScope(); }, [load, loadScope]);

  React.useEffect(() => {
    if (form.match_type !== "regex" || !form.pattern) { setRegexErr(""); setRegexWarn(false); return; }
    try {
      new RegExp(form.pattern);
      setRegexErr("");
      setRegexWarn(false);
    } catch {
      // JS RegExp tidak mendukung sintaks ekstensi Go RE2 (seperti flag inline (?i)).
      // Ini hanya kegagalan validasi pratinjau, bukan berarti tidak valid di sisi Go; validasi akhir diserahkan ke server.
      setRegexErr("");
      setRegexWarn(true);
    }
  }, [form.pattern, form.match_type]);

  // ---- rule handlers ----

  function set(patch: Partial<RuleForm>) { setForm(f => ({ ...f, ...patch })); }

  function openNew() {
    setEditing(null);
    setForm(defaultForm());
    setRegexErr("");
    setOpen(true);
  }

  function openEdit(rule: InterceptRule) {
    setEditing(rule);
    setForm({
      name: rule.name, enabled: rule.enabled, priority: rule.priority,
      match_target: rule.match_target, match_type: rule.match_type,
      pattern: rule.pattern, action: rule.action, message: rule.message,
      timeout_enabled: rule.timeout_enabled, timeout_seconds: rule.timeout_seconds,
      timeout_action: rule.timeout_action,
    });
    setRegexErr("");
    setOpen(true);
  }

  async function handleSave() {
    if (!form.name.trim())    { toast.error("Nama tidak boleh kosong"); return; }
    if (!form.pattern.trim()) { toast.error("Pola tidak boleh kosong"); return; }
    if (regexErr)             { toast.error("Sintaks regex tidak valid"); return; }
    setSaving(true);
    try {
      if (editing) {
        await api.updateInterceptRule(editing.id, form);
        toast.success("Aturan berhasil diperbarui");
      } else {
        await api.createInterceptRule(form);
        toast.success("Aturan berhasil dibuat");
      }
      setOpen(false);
      load();
    } catch (e) {
      toast.error((e as Error).message);
    } finally {
      setSaving(false);
    }
  }

  async function handleDelete(id: number) {
    try {
      await api.deleteInterceptRule(id);
      toast.success("Aturan berhasil dihapus");
      load();
    } catch (e) {
      toast.error((e as Error).message);
    }
  }

  async function handleToggle(rule: InterceptRule) {
    try {
      await api.toggleInterceptRule(rule.id, !rule.enabled);
      load();
    } catch (e) {
      toast.error((e as Error).message);
    }
  }

  // ---- scope handlers ----

  async function openScope() {
    setScopeOpen(true);
    setScopeLoading(true);
    try {
      const [tools, cfg] = await Promise.all([api.tools(), api.interceptGetToolConfig()]);
      setAllTools(tools);
      setEnabledTools(new Set(cfg.enabled_tools));
    } catch (e) {
      toast.error("Gagal memuat: " + (e as Error).message);
    } finally {
      setScopeLoading(false);
    }
  }

  function toggleTool(key: string, val: boolean) {
    setEnabledTools(prev => {
      const next = new Set(prev);
      if (val) next.add(key); else next.delete(key);
      return next;
    });
  }

  async function saveScope() {
    setScopeSaving(true);
    try {
      await api.interceptSetToolConfig([...enabledTools]);
      toast.success("Cakupan intersepsi berhasil disimpan");
      setScopeTools([...enabledTools]);
      setScopeOpen(false);
    } catch (e) {
      toast.error("Gagal menyimpan: " + (e as Error).message);
    } finally {
      setScopeSaving(false);
    }
  }

  const toolGroups = React.useMemo(() => groupTools(allTools), [allTools]);

  // ---- render ----

  return (
    <div className="flex flex-1 flex-col gap-5 p-6">

      {/* ---- header ---- */}
      <div className="flex items-center gap-2.5">
        <ShieldAlertIcon className="h-5 w-5 shrink-0" />
        <div>
          <h1 className="text-lg font-semibold leading-tight">Intersepsi Command</h1>
          <p className="text-sm text-muted-foreground mt-0.5">
            Sebelum tool dieksekusi, dicocokkan dulu dengan aturan intersepsi; command yang tidak cocok dapat diserahkan ke penentuan cadangan oleh model
          </p>
        </div>
      </div>

      {/* ---- Bar informasi cakupan intersepsi (dipakai bersama oleh pencocokan aturan dan persetujuan cadangan model: tool di luar cakupan tidak disentuh oleh keduanya) ---- */}
      <div
        className={`flex items-center justify-between gap-3 rounded-lg border px-4 py-2.5 ${
          scopeTools.length === 0
            ? "border-amber-400/60 bg-amber-50/50 dark:bg-amber-950/20"
            : "bg-muted/40"
        }`}
      >
        <div className="flex min-w-0 items-center gap-2 text-sm">
          <ListFilterIcon className="h-4 w-4 shrink-0 text-muted-foreground" />
          <span className="shrink-0 font-medium">Cakupan Intersepsi</span>
          {scopeTools.length === 0 ? (
            <span className="text-amber-700 dark:text-amber-500">
              Belum ada tool yang diaktifkan, aturan intersepsi maupun persetujuan cadangan model tidak akan berlaku
            </span>
          ) : (
            <>
              <Badge variant="secondary" className="shrink-0">{scopeTools.length} tool</Badge>
              <span className="truncate text-muted-foreground" title={scopeTools.join("、")}>
                {scopeTools.join("、")}
              </span>
            </>
          )}
        </div>
        <Button
          variant={scopeTools.length === 0 ? "default" : "outline"}
          size="sm"
          className="shrink-0"
          onClick={openScope}
        >
          <ListFilterIcon className="h-4 w-4" />
          Atur Cakupan
        </Button>
      </div>

      <Tabs defaultValue="rules" className="flex-1">
        <TabsList>
          <TabsTrigger value="rules">Aturan Intersepsi</TabsTrigger>
          <TabsTrigger value="judge">Konfigurasi Model</TabsTrigger>
        </TabsList>

        {/* ---- tab: Aturan Intersepsi ---- */}
        <TabsContent value="rules" className="mt-4 flex flex-col gap-4">
          <div className="flex items-center justify-between gap-3">
            <p className="text-xs text-muted-foreground">
              Dicocokkan satu per satu berdasarkan prioritas (angka lebih besar diperiksa lebih dulu), aturan pertama yang cocok akan berlaku
            </p>
            <Button onClick={openNew} size="sm" className="shrink-0">
              <PlusIcon className="h-4 w-4" />
              Buat Aturan Baru
            </Button>
          </div>

          <Card>
            <CardContent className="p-0">
          {loading ? (
            <p className="p-6 text-sm text-muted-foreground">Memuat...</p>
          ) : rules.length === 0 ? (
            <div className="flex flex-col items-center justify-center gap-2 py-16 text-center">
              <ShieldAlertIcon className="h-8 w-8 text-muted-foreground/40" />
              <p className="text-sm text-muted-foreground">Belum ada aturan</p>
              <Button size="sm" variant="outline" onClick={openNew}>
                <PlusIcon className="h-4 w-4" />
                Buat Aturan Pertama
              </Button>
            </div>
          ) : (
            <Table>
              <TableHeader>
                <TableRow className="hover:bg-transparent">
                  <TableHead className="w-[72px]">Prioritas</TableHead>
                  <TableHead>Nama</TableHead>
                  <TableHead className="w-[90px]">Target</TableHead>
                  <TableHead className="w-[80px]">Tipe</TableHead>
                  <TableHead>Pola</TableHead>
                  <TableHead className="w-[72px]">Aksi</TableHead>
                  <TableHead className="w-[64px] text-center">Aktif</TableHead>
                  <TableHead className="w-[80px]" />
                </TableRow>
              </TableHeader>
              <TableBody>
                {rules.map((rule) => (
                  <TableRow key={rule.id} className={!rule.enabled ? "opacity-40" : ""}>
                    <TableCell>
                      <span className="font-mono text-xs tabular-nums">{rule.priority}</span>
                    </TableCell>
                    <TableCell className="font-medium text-sm">{rule.name}</TableCell>
                    <TableCell>
                      <span className="text-xs text-muted-foreground">
                        {rule.match_target === "tool_name" ? "Nama Tool" : "Isi Input"}
                      </span>
                    </TableCell>
                    <TableCell>
                      <span className="text-xs text-muted-foreground">
                        {rule.match_type === "regex" ? "Regex" : "String"}
                      </span>
                    </TableCell>
                    <TableCell className="max-w-[220px]">
                      <code className="block truncate rounded bg-muted px-1.5 py-0.5 text-xs font-mono">
                        {rule.pattern}
                      </code>
                    </TableCell>
                    <TableCell>
                      <ActionBadge action={rule.action} />
                    </TableCell>
                    <TableCell className="text-center">
                      <Switch
                        checked={rule.enabled}
                        onCheckedChange={() => handleToggle(rule)}
                      />
                    </TableCell>
                    <TableCell>
                      <div className="flex items-center justify-end gap-0.5">
                        <Button
                          size="icon" variant="ghost" className="h-7 w-7"
                          onClick={() => openEdit(rule)}
                        >
                          <PencilIcon className="h-3.5 w-3.5" />
                        </Button>
                        <Button
                          size="icon" variant="ghost"
                          className="h-7 w-7 text-destructive hover:text-destructive"
                          onClick={() => handleDelete(rule.id)}
                        >
                          <Trash2Icon className="h-3.5 w-3.5" />
                        </Button>
                      </div>
                    </TableCell>
                  </TableRow>
                ))}
              </TableBody>
            </Table>
          )}
            </CardContent>
          </Card>
        </TabsContent>

        {/* ---- tab: Konfigurasi Model ---- */}
        <TabsContent value="judge" className="mt-4">
          <JudgeCard />
        </TabsContent>
      </Tabs>

      {/* ---- editor sheet ---- */}
      <Sheet open={open} onOpenChange={setOpen}>
        <SheetContent side="right" className="flex flex-col gap-0 p-0 sm:max-w-md">
          <SheetHeader className="border-b px-6 py-4">
            <SheetTitle>{editing ? "Edit Aturan" : "Buat Aturan Baru"}</SheetTitle>
            <SheetDescription className="text-xs">
              Prioritas lebih besar dicocokkan lebih dulu; aturan pertama yang cocok berlaku, sisanya dilewati
            </SheetDescription>
          </SheetHeader>

          <div className="flex-1 min-h-0 overflow-y-auto px-6 py-5 space-y-5">
            <Field label="Nama">
              <Input
                placeholder="Beri nama untuk aturan ini"
                value={form.name}
                onChange={(e) => set({ name: e.target.value })}
              />
            </Field>

            <Field label="Prioritas (angka lebih besar dicocokkan lebih dulu)">
              <Input
                type="number"
                value={form.priority}
                onChange={(e) => set({ priority: parseInt(e.target.value) || 0 })}
              />
            </Field>

            <Separator />

            <Field label="Target Pencocokan">
              <Select
                value={form.match_target}
                onValueChange={(v) => set({ match_target: v as RuleForm["match_target"] })}
              >
                <SelectTrigger><SelectValue /></SelectTrigger>
                <SelectContent>
                  <SelectItem value="tool_name">Nama Tool (tool_name)</SelectItem>
                  <SelectItem value="tool_input">Isi Input (tool_input JSON)</SelectItem>
                </SelectContent>
              </Select>
            </Field>

            <Field label="Tipe Pencocokan">
              <Select
                value={form.match_type}
                onValueChange={(v) => set({ match_type: v as RuleForm["match_type"] })}
              >
                <SelectTrigger><SelectValue /></SelectTrigger>
                <SelectContent>
                  <SelectItem value="string">Mengandung String</SelectItem>
                  <SelectItem value="regex">Regular Expression</SelectItem>
                </SelectContent>
              </Select>
            </Field>

            <Field label="Pola">
              <Input
                placeholder={form.match_type === "regex" ? "^Bash$" : "rm -rf"}
                value={form.pattern}
                onChange={(e) => set({ pattern: e.target.value })}
                className={regexErr ? "border-destructive focus-visible:ring-destructive" : ""}
              />
              {regexErr && (
                <p className="text-xs text-destructive mt-1">{regexErr}</p>
              )}
              {regexWarn && (
                <p className="text-xs text-amber-600 mt-1">Mengandung sintaks ekstensi Go RE2 (seperti <code className="font-mono">(?i)</code>), browser tidak bisa menampilkan pratinjau, akan divalidasi oleh server setelah dikirim</p>
              )}
            </Field>

            <Separator />

            <Field label="Aksi Intersepsi">
              <Select
                value={form.action}
                onValueChange={(v) => set({ action: v as InterceptAction })}
              >
                <SelectTrigger><SelectValue /></SelectTrigger>
                <SelectContent>
                  <SelectItem value="allow">Izinkan, langsung diloloskan, melewati aturan berikutnya</SelectItem>
                  <SelectItem value="deny">Larang, diblokir, mengirim pesan penolakan ke model</SelectItem>
                  <SelectItem value="ask">Minta ke pengguna, menunggu persetujuan</SelectItem>
                </SelectContent>
              </Select>
            </Field>

            {form.action !== "allow" && (
              <Field label={form.action === "deny" ? "Pesan Penolakan (dikirim ke model)" : "Catatan Persetujuan (opsional)"}>
                <Textarea
                  placeholder={form.action === "deny" ? "Operasi diblokir oleh kebijakan keamanan" : ""}
                  value={form.message}
                  onChange={(e) => set({ message: e.target.value })}
                  rows={2}
                  className="resize-none"
                />
              </Field>
            )}

            {form.action === "ask" && (
              <>
                <Separator />
                <div className="flex items-center justify-between">
                  <div>
                    <p className="text-sm font-medium">Aktifkan Timeout Persetujuan</p>
                    <p className="text-xs text-muted-foreground">Ditangani otomatis setelah timeout, tidak menunggu lagi</p>
                  </div>
                  <Switch
                    checked={form.timeout_enabled}
                    onCheckedChange={(v) => set({ timeout_enabled: v })}
                  />
                </div>
                {form.timeout_enabled && (
                  <div className="flex items-end gap-3">
                    <Field label="Durasi Timeout (detik)">
                      <Input
                        type="number"
                        min={5}
                        className="w-28"
                        value={form.timeout_seconds}
                        onChange={(e) => {
                          const n = parseInt(e.target.value, 10);
                          if (n > 0) set({ timeout_seconds: n });
                        }}
                      />
                    </Field>
                    <Field label="Aksi Setelah Timeout">
                      <Select
                        value={form.timeout_action}
                        onValueChange={(v) => set({ timeout_action: v as "deny" | "allow" })}
                      >
                        <SelectTrigger className="w-32"><SelectValue /></SelectTrigger>
                        <SelectContent>
                          <SelectItem value="deny">Tolak Otomatis</SelectItem>
                          <SelectItem value="allow">Izinkan Otomatis</SelectItem>
                        </SelectContent>
                      </Select>
                    </Field>
                  </div>
                )}
              </>
            )}

            <Separator />

            <div className="flex items-center gap-3">
              <Switch
                id="rule-enabled"
                checked={form.enabled}
                onCheckedChange={(v) => set({ enabled: v })}
              />
              <Label htmlFor="rule-enabled" className="cursor-pointer">Aktifkan aturan ini</Label>
            </div>
          </div>

          <SheetFooter className="border-t px-6 py-4 flex-row justify-end gap-2">
            <Button variant="outline" onClick={() => setOpen(false)}>Batal</Button>
            <Button onClick={handleSave} disabled={saving || !!regexErr}>
              {saving ? "Menyimpan..." : "Simpan"}
            </Button>
          </SheetFooter>
        </SheetContent>
      </Sheet>

      {/* ---- scope dialog ---- */}
      <Dialog open={scopeOpen} onOpenChange={setScopeOpen}>
        <DialogContent className="sm:max-w-lg flex flex-col overflow-hidden p-0 gap-0" style={{ maxHeight: "min(80vh, 560px)" }}>
          <DialogHeader className="shrink-0 border-b px-6 py-4">
            <DialogTitle className="flex items-center gap-2">
              <ListFilterIcon className="h-4 w-4" />
              Cakupan Intersepsi
            </DialogTitle>
            <DialogDescription className="text-xs">
              Hanya tool yang intersepsinya diaktifkan yang masuk ke pencocokan aturan; tool lainnya langsung diloloskan
            </DialogDescription>
          </DialogHeader>

          <div className="flex-1 min-h-0 overflow-y-auto px-6 py-4 space-y-5">
            {scopeLoading ? (
              <p className="text-sm text-muted-foreground py-4">Memuat...</p>
            ) : (
              toolGroups.map((group, gi) => (
                <div key={group.label}>
                  {gi > 0 && <Separator className="mb-5" />}
                  <p className="text-[10px] font-semibold text-muted-foreground uppercase tracking-wider mb-2">
                    {group.label}
                  </p>
                  <div className="space-y-0.5">
                    {group.tools.map((t) => (
                      <div key={t.key} className="flex items-center gap-3 rounded-md px-2 py-1.5 hover:bg-muted/50">
                        <Switch
                          id={`scope-${t.key}`}
                          checked={enabledTools.has(t.key)}
                          onCheckedChange={(v) => toggleTool(t.key, v)}
                        />
                        <label htmlFor={`scope-${t.key}`} className="flex-1 min-w-0 cursor-pointer">
                          <div className="flex items-center gap-1.5">
                            <span className="font-mono text-sm">{t.key}</span>
                            {(t.kind && t.kind !== "builtin") && (
                              <Badge variant="outline" className="px-1 py-0 text-[10px]">{t.kind}</Badge>
                            )}
                          </div>
                          {t.description && (
                            <p className="text-[11px] text-muted-foreground line-clamp-1">{t.description}</p>
                          )}
                        </label>
                      </div>
                    ))}
                  </div>
                </div>
              ))
            )}
          </div>

          <div className="shrink-0 border-t px-6 py-3 flex justify-end gap-2">
            <Button variant="outline" size="sm" onClick={() => setScopeOpen(false)}>Batal</Button>
            <Button size="sm" onClick={saveScope} disabled={scopeSaving || scopeLoading}>
              {scopeSaving ? "Menyimpan..." : "Simpan"}
            </Button>
          </div>
        </DialogContent>
      </Dialog>
    </div>
  );
}
