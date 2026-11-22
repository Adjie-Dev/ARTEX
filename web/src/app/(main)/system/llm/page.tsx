"use client";

import * as React from "react";

import {
  Loader2Icon,
  PlugZapIcon,
  PlusIcon,
  RefreshCwIcon,
  RotateCcwIcon,
  SaveIcon,
  StarIcon,
  Trash2Icon,
  ZapIcon,
} from "lucide-react";
import { toast } from "sonner";

import { Badge } from "@/components/ui/badge";
import { Button } from "@/components/ui/button";
import { Card, CardContent } from "@/components/ui/card";
import { Input } from "@/components/ui/input";
import { Label } from "@/components/ui/label";
import { Popover, PopoverContent, PopoverTrigger } from "@/components/ui/popover";
import { Select, SelectContent, SelectItem, SelectTrigger, SelectValue } from "@/components/ui/select";
import { Separator } from "@/components/ui/separator";
import { Sheet, SheetContent, SheetDescription, SheetHeader, SheetTitle } from "@/components/ui/sheet";
import { Switch } from "@/components/ui/switch";
import { Tabs, TabsContent, TabsList, TabsTrigger } from "@/components/ui/tabs";
import { api } from "@/lib/api";
import type { LLMPoolMember, LLMPoolStatus, LLMProfile, LLMRetryOverride } from "@/lib/types";
import { cn } from "@/lib/utils";

import { ProfileRetryFields, RetryPolicyPanel, ZERO_OVERRIDE } from "./_components/retry";

// Thinking switch (thinking.type) dan reasoning intensity (reasoning_effort) adalah dua field yang SALING INDEPENDEN,
// masing-masing diatur terpisah. Sebagian API tidak punya field thinking dan hanya mengandalkan parameter
// intensitas untuk mengaktifkan thinking, makanya harus dipisah.
// String kosong di database berarti field ini TIDAK DIKIRIM; Radix Select tidak menerima value kosong, makanya
// UI memakai "none" sebagai penanda tidak dikirim, dikonversi bolak-balik dengan "" saat disimpan/dibaca (NONE / fromStore / toStore).
const NONE = "none";
const fromStore = (v?: string) => (v ? v : NONE);
const toStore = (v: string) => (v === NONE ? "" : v);
const THINKING_TYPES: { value: string; label: string }[] = [
  { value: NONE, label: "Tidak dikirim (default)" },
  { value: "disabled", label: "Nonaktif" },
  { value: "enabled", label: "Aktif" },
];
// Nama field request mana yang dipakai untuk batas output (hanya relevan untuk format openai). NONE ↔ "" pakai konversi penanda yang sama.
const MAX_TOKENS_FIELDS: { value: string; label: string }[] = [
  { value: NONE, label: "max_tokens (default)" },
  { value: "max_completion_tokens", label: "max_completion_tokens" },
];
// Dua format lainnya sudah punya nama field yang tetap, pilihan ini tidak relevan untuk keduanya, penjelasannya langsung ditulis di teks bantuan.
const MAX_TOKENS_FIELD_HINTS: Record<string, string> = {
  openai:
    "Key mana yang dikirim untuk batas ini. max_tokens adalah default, hampir semua gateway kompatibel hanya mengenali ini; model reasoning resmi OpenAI (seri o / GPT-5) justru hanya mengenali max_completion_tokens, kalau menerima max_tokens akan langsung error unsupported_parameter.",
  anthropic: "Hanya bisa dipilih untuk format openai. Nama field Anthropic tetap max_tokens.",
  "openai-responses": "Hanya bisa dipilih untuk format openai. Nama field Responses API tetap max_output_tokens.",
};
const EFFORT_LEVELS: { value: string; label: string }[] = [
  { value: NONE, label: "Tidak dikirim (default)" },
  { value: "low", label: "low" },
  { value: "medium", label: "medium" },
  { value: "high", label: "high" },
  { value: "xhigh", label: "xhigh" },
  { value: "max", label: "max" },
];

function cooldownText(secs: number) {
  if (secs <= 0) return "";
  if (secs < 60) return `${secs}s`;
  return `${Math.ceil(secs / 60)}min`;
}

// Status "normal atau tidak" yang ditampilkan di kartu untuk sebuah konfigurasi. Konfigurasi yang belum diisi
// Key sama sekali tidak bisa mengirim request, jadi ini lebih perlu disebutkan duluan daripada circuit breaker;
// status lainnya berasal dari catatan circuit breaker hasil polling (saat polling mati tidak akan ada catatan
// baru, jadi "normal" di sini berarti tidak ada kegagalan yang diketahui).
type Health = { label: string; cls: string; hint?: string };
function healthOf(p: LLMProfile, m?: LLMPoolMember): Health {
  if (!p.api_key_hint) {
    return {
      label: "Key Belum Dikonfigurasi",
      cls: "border-muted-foreground/40 text-muted-foreground",
      hint: "API Key belum diisi, tidak bisa dipanggil",
    };
  }
  if (m?.state === "tripped") {
    return {
      label: m.cooldown_secs > 0 ? `Terputus · ${cooldownText(m.cooldown_secs)}` : "Terputus",
      cls: "border-destructive/50 text-destructive",
      hint: m.last_error,
    };
  }
  if (m?.state === "degraded") {
    return {
      label: `Bermasalah · Gagal ${m.fails} kali`,
      cls: "border-amber-500/50 text-amber-600 dark:text-amber-400",
      hint: m.last_error,
    };
  }
  return { label: "Normal", cls: "border-emerald-500/50 text-emerald-600 dark:text-emerald-400" };
}

// ─────────────────────────────────────────────────────────────────────────────
// Drawer Konfigurasi Rotasi
// ─────────────────────────────────────────────────────────────────────────────

function PoolSheet({
  open,
  onOpenChange,
  pool,
  onReload,
}: {
  open: boolean;
  onOpenChange: (o: boolean) => void;
  pool: LLMPoolStatus | null;
  onReload: () => Promise<void>;
}) {
  const [busy, setBusy] = React.useState(false);

  // Hitung mundur cooldown adalah sisa detik yang dihitung backend, hanya ditarik berkala saat drawer terbuka dan ada konfigurasi yang tidak normal, supaya jalan terus.
  React.useEffect(() => {
    if (!open || !pool?.enabled || !pool.chain.some((m) => m.state !== "ok")) return;
    const t = setInterval(() => void onReload(), 10_000);
    return () => clearInterval(t);
  }, [open, pool, onReload]);

  async function toggle(patch: { llm_pool_enabled?: boolean; llm_pool_bind_fallback?: boolean }) {
    if (busy) return;
    setBusy(true);
    try {
      await api.setSettings(patch);
      await onReload();
      if (patch.llm_pool_enabled !== undefined) {
        toast.success(patch.llm_pool_enabled ? "Rotasi LLM diaktifkan" : "Rotasi LLM dinonaktifkan");
      } else {
        toast.success("Pengaturan fallback diperbarui");
      }
    } catch (e) {
      toast.error(`Gagal menyimpan pengaturan: ${(e as Error).message}`);
    } finally {
      setBusy(false);
    }
  }

  async function recover(id?: string) {
    try {
      await api.resetLLMPool(id);
      await onReload();
      toast.success(id ? "Konfigurasi berhasil dipulihkan" : "Semua konfigurasi berhasil dipulihkan");
    } catch (e) {
      toast.error(`Gagal memulihkan: ${(e as Error).message}`);
    }
  }

  const enabled = pool?.enabled ?? false;
  const chain = pool?.chain ?? [];
  // Anggota yang ikut rotasi (mengecualikan yang ditandai "tidak ikut rotasi"), urutannya sama dengan urutan percobaan backend yang sesungguhnya.
  const inChain = chain.filter((m) => m.active || !m.excluded);
  const tripped = chain.filter((m) => m.state === "tripped");

  return (
    <Sheet open={open} onOpenChange={onOpenChange}>
      <SheetContent side="right" className="flex flex-col gap-0 p-0 data-[side=right]:sm:max-w-lg">
        <SheetHeader className="px-4">
          <SheetTitle className="flex items-center gap-2">
            <ZapIcon className="size-4" /> Rotasi LLM · Failover
          </SheetTitle>
          <SheetDescription>
            Jika diaktifkan, Agent yang <b>tidak menentukan model tertentu</b> akan otomatis beralih ke konfigurasi
            berikutnya saat konfigurasi saat ini tidak tersedia (saldo habis / Key tidak valid / kena rate limit /
            layanan bermasalah).
          </SheetDescription>
        </SheetHeader>

        <div className="flex min-h-0 flex-1 flex-col gap-4 overflow-y-auto px-4 pb-6">
          <div className="flex items-center justify-between gap-4 rounded-lg border p-3">
            <div className="grid gap-0.5">
              <Label className="text-sm">Aktifkan Rotasi</Label>
              <p className="text-muted-foreground text-xs">Nonaktif secara default. Saat nonaktif, selalu hanya memakai konfigurasi yang aktif, jika gagal ya gagal.</p>
            </div>
            <Switch
              checked={enabled}
              disabled={busy}
              onCheckedChange={(v) => void toggle({ llm_pool_enabled: v })}
              aria-label="Sakelar Rotasi LLM"
            />
          </div>

          {enabled && (
            <>
              <div className="flex items-center justify-between gap-4 rounded-lg border p-3">
                <div className="grid gap-0.5">
                  <Label className="text-sm">Tetap Fallback Meski Model Ditentukan</Label>
                  <p className="text-muted-foreground text-xs">
                    Nonaktif secara default: jika Agent atau tugas menentukan konfigurasi tertentu, hanya konfigurasi
                    itu yang dipakai, jika gagal ya gagal (tidak diam-diam diganti ke model lain). Jika diaktifkan,
                    konfigurasi yang ditentukan juga akan fallback ke rantai rotasi di bawah saat gagal.
                  </p>
                </div>
                <Switch
                  checked={pool?.bind_fallback ?? false}
                  disabled={busy}
                  onCheckedChange={(v) => void toggle({ llm_pool_bind_fallback: v })}
                  aria-label="Sakelar fallback saat konfigurasi terikat gagal"
                />
              </div>

              <Separator />

              <div className="grid gap-2">
                <div className="flex items-center justify-between">
                  <Label className="text-sm">Urutan Rotasi</Label>
                  {tripped.length > 0 && (
                    <Button size="sm" variant="ghost" onClick={() => void recover()}>
                      <RotateCcwIcon /> Pulihkan Semua
                    </Button>
                  )}
                </div>
                {inChain.length < 2 && (
                  <p className="text-muted-foreground text-xs">
                    Saat ini hanya ada {inChain.length} konfigurasi yang tersedia, rotasi tidak akan berjalan.
                    Dibutuhkan minimal 2 konfigurasi yang sudah diisi API Key dan ikut rotasi.
                  </p>
                )}
                {chain.map((m) => {
                  const excluded = m.excluded && !m.active;
                  const order = excluded ? null : inChain.findIndex((x) => x.profile_id === m.profile_id) + 1;
                  return (
                    <div
                      key={m.profile_id}
                      className={cn(
                        "grid gap-1 rounded-lg border p-2.5 text-sm",
                        excluded && "opacity-55",
                        m.state === "tripped" && "border-destructive/40",
                      )}
                    >
                      <div className="flex flex-wrap items-center gap-x-2 gap-y-1">
                        <span className="w-5 shrink-0 text-center font-mono text-muted-foreground text-xs">
                          {order ?? "—"}
                        </span>
                        <span className="font-medium">{m.name}</span>
                        {m.active && (
                          <Badge variant="outline" className="border-amber-400/50 text-amber-500">
                            Aktif
                          </Badge>
                        )}
                        {excluded && <Badge variant="outline">Tidak Ikut Rotasi</Badge>}
                        <div className="ml-auto flex items-center gap-2">
                          {m.state === "tripped" && m.cooldown_secs > 0 && (
                            <span className="text-muted-foreground text-xs">Cooldown {cooldownText(m.cooldown_secs)}</span>
                          )}
                          {m.state === "degraded" && (
                            <span className="text-muted-foreground text-xs">Gagal berturut-turut {m.fails} kali</span>
                          )}
                          {m.state !== "ok" && (
                            <Button
                              size="icon"
                              variant="ghost"
                              className="size-7"
                              aria-label="Pulihkan Sekarang"
                              title="Pulihkan sekarang: hapus status terputus, panggilan berikutnya akan mencoba lagi konfigurasi ini"
                              onClick={() => void recover(m.profile_id)}
                            >
                              <RotateCcwIcon className="size-3.5" />
                            </Button>
                          )}
                        </div>
                      </div>
                      <div className="flex flex-wrap items-center gap-x-3 pl-7 text-muted-foreground text-xs">
                        <code className="truncate font-mono">{m.model}</code>
                        {!m.active && <span>Prioritas {m.priority}</span>}
                      </div>
                      {m.last_error && (
                        <p className="truncate pl-7 font-mono text-muted-foreground text-xs" title={m.last_error}>
                          {m.last_error}
                        </p>
                      )}
                    </div>
                  );
                })}
                {chain.length === 0 && (
                  <div className="rounded-lg border border-dashed p-4 text-center text-muted-foreground text-sm">
                    Belum ada konfigurasi
                  </div>
                )}
              </div>

              <div className="rounded-lg border border-dashed p-3 text-muted-foreground text-xs leading-relaxed">
                Konfigurasi yang aktif selalu berada di urutan pertama, sisanya diurutkan dari prioritas tertinggi ke
                terendah (diatur di masing-masing konfigurasi). Konfigurasi yang gagal akan masuk masa cooldown (60
                detik → 5 menit → 30 menit), dilewati selama masa cooldown, dan otomatis dipakai lagi setelah pulih.
                Konfigurasi yang context window-nya tidak cukup untuk request saat ini akan dilewati. Agent dan tugas
                yang sudah menentukan model secara default tidak ikut rotasi.
              </div>
            </>
          )}
        </div>
      </SheetContent>
    </Sheet>
  );
}

// ─────────────────────────────────────────────────────────────────────────────
// Drawer Konfigurasi Model (form yang sama dipakai untuk membuat baru / mengedit)
// ─────────────────────────────────────────────────────────────────────────────

function ProfileSheet({
  profile,
  open,
  onOpenChange,
  onSaved,
}: {
  profile: LLMProfile | null; // null = buat baru
  open: boolean;
  onOpenChange: (o: boolean) => void;
  onSaved: (id: string) => void;
}) {
  const isNew = !profile;
  const [name, setName] = React.useState("");
  const [format, setFormat] = React.useState<"anthropic" | "openai" | "openai-responses">("anthropic");
  const [model, setModel] = React.useState("");
  const [baseUrl, setBaseUrl] = React.useState("");
  const [proxy, setProxy] = React.useState("");
  const [apiKey, setApiKey] = React.useState("");
  const [keyHint, setKeyHint] = React.useState("");
  const [rps, setRps] = React.useState("0");
  const [rpm, setRpm] = React.useState("0");
  const [cw, setCw] = React.useState("0"); // Context window (K token); 0 = default 200K
  const [thinkingType, setThinkingType] = React.useState(NONE);
  const [effort, setEffort] = React.useState(NONE);
  const [priority, setPriority] = React.useState("0"); // Urutan rotasi; makin besar makin didahulukan
  const [poolExclude, setPoolExclude] = React.useState(false);
  const [streaming, setStreaming] = React.useState(true); // true = streaming (default); false = non-streaming
  const [maxTokens, setMaxTokens] = React.useState("0"); // Batas output per balasan; 0 = tidak dikirim
  const [maxTokensField, setMaxTokensField] = React.useState(NONE); // Nama field yang dipakai untuk batas ini; NONE = max_tokens
  const [sessionHeaderKey, setSessionHeaderKey] = React.useState(""); // Nama header sesi kustom; kosong = tidak dikirim
  const [retry, setRetry] = React.useState<LLMRetryOverride>(ZERO_OVERRIDE); // Override retry untuk konfigurasi ini; semua 0 = ikut pengaturan global
  const [testing, setTesting] = React.useState(false);
  const [saving, setSaving] = React.useState(false);
  const [models, setModels] = React.useState<string[]>([]);
  const [loadingModels, setLoadingModels] = React.useState(false);
  const [modelsOpen, setModelsOpen] = React.useState(false);

  // Setiap kali dibuka, form diisi ulang dari profile yang diterima (kalau buat baru, direset ke nilai default).
  // Drawer yang ditutup lalu dibuka lagi selalu mulai bersih, tidak menyisakan bekas konfigurasi sebelumnya.
  React.useEffect(() => {
    if (!open) return;
    setName(profile?.name ?? "");
    setFormat(profile?.format === "openai" || profile?.format === "openai-responses" ? profile.format : "anthropic");
    setModel(profile?.model ?? "");
    setBaseUrl(profile?.base_url ?? "");
    setProxy(profile?.proxy ?? "");
    setRps(String(profile?.rate_per_second ?? 0));
    setRpm(String(profile?.rate_per_minute ?? 0));
    setCw(String(profile?.context_window_k ?? 0));
    setThinkingType(fromStore(profile?.thinking_type));
    setEffort(fromStore(profile?.reasoning_effort));
    setPriority(String(profile?.priority ?? 0));
    setPoolExclude(profile?.pool_exclude ?? false);
    setStreaming(profile?.streaming ?? true);
    setMaxTokens(String(profile?.max_tokens ?? 0));
    setMaxTokensField(fromStore(profile?.max_tokens_field));
    setSessionHeaderKey(profile?.session_header_key ?? "");
    setRetry(profile?.retry ?? ZERO_OVERRIDE);
    setApiKey("");
    setKeyHint(profile?.api_key_hint ?? "");
    setModels([]);
    setModelsOpen(false);
  }, [open, profile]);

  const profileId = profile ? Number(profile.id) : undefined;

  async function loadModels() {
    if (loadingModels) return;
    setLoadingModels(true);
    setModels([]);
    try {
      const r = await api.fetchLLMModels(format, baseUrl, apiKey, proxy, profileId);
      if (r.ok && r.models && r.models.length > 0) {
        setModels(r.models);
        setModelsOpen(true);
        toast.success(`Berhasil memuat ${r.models.length} model`);
      } else {
        toast.error(`Gagal memuat model: ${r.error ?? "Model tidak ditemukan"}`);
      }
    } catch (e) {
      toast.error(`Terjadi error saat memuat model: ${(e as Error).message}`);
    } finally {
      setLoadingModels(false);
    }
  }

  async function testConnection() {
    if (testing) return;
    setTesting(true);
    try {
      // Diuji dengan parameter thinking yang sama persis dengan yang akan dipakai saat konfigurasi berjalan,
      // supaya model yang tidak mendukung field ini langsung gagal di sini, bukan meledak saat tugas sedang
      // berjalan. profile id dikirim supaya bisa pakai Key yang sudah tersimpan saat kolom Key dikosongkan.
      const r = await api.testLLM(
        format,
        model,
        baseUrl,
        apiKey,
        proxy,
        toStore(thinkingType),
        toStore(effort),
        profileId,
        streaming,
        sessionHeaderKey.trim(),
      );
      // Isi balasan ikut ditampilkan: baru terasa setara dengan jalan di sesi sungguhan kalau terlihat modelnya benar-benar merespons.
      if (r.ok)
        toast.success(`Koneksi berhasil · ${r.latency_ms ?? "?"}ms · ${r.model ?? model}`, {
          description: r.reply ? `Balasan: ${r.reply}` : undefined,
        });
      else toast.error(`Koneksi gagal: ${r.error ?? "Tidak diketahui"}`);
    } catch (e) {
      toast.error(`Terjadi error saat pengujian: ${(e as Error).message}`);
    } finally {
      setTesting(false);
    }
  }

  async function save() {
    if (!name.trim() || !model.trim()) {
      toast.error("Isi nama dan model terlebih dahulu");
      return;
    }
    if (saving) return;
    setSaving(true);
    try {
      const { id } = await api.saveLLMProfile({
        ...(profile ? { id: Number(profile.id) } : {}),
        name: name.trim(),
        format,
        model: model.trim(),
        base_url: baseUrl.trim(),
        proxy: proxy.trim(),
        api_key: apiKey,
        rate_per_second: Number(rps) || 0,
        rate_per_minute: Number(rpm) || 0,
        context_window_k: Number(cw) || 0,
        thinking_type: toStore(thinkingType),
        reasoning_effort: toStore(effort),
        priority: Number(priority) || 0,
        pool_exclude: poolExclude,
        streaming,
        max_tokens: Math.max(0, Number(maxTokens) || 0),
        // Sakelar nama field hanya relevan untuk openai (Chat Completions), format lain selalu kembali ke default;
        // backend juga akan melakukan normalisasi yang sama, ini hanya supaya UI tidak mengirim nilai yang saling bertentangan.
        max_tokens_field: format === "openai" ? toStore(maxTokensField) : "",
        session_header_key: sessionHeaderKey.trim(),
        retry,
      });
      if (isNew) toast.success(`Berhasil dibuat: ${name.trim()} (klik "Jadikan Aktif" pada kartu untuk mengaktifkannya)`);
      else toast.success(profile?.is_default ? "Tersimpan, konfigurasi aktif langsung berlaku tanpa perlu restart" : "Tersimpan");
      onSaved(String(id));
      onOpenChange(false);
    } catch (e) {
      toast.error(`Gagal menyimpan: ${(e as Error).message}`);
    } finally {
      setSaving(false);
    }
  }

  return (
    <Sheet open={open} onOpenChange={onOpenChange}>
      <SheetContent
        side="right"
        className="flex flex-col gap-0 p-0 data-[side=right]:min-w-[420px] data-[side=right]:sm:max-w-xl"
      >
        <SheetHeader className="px-4">
          <SheetTitle className="flex items-center gap-2">
            {isNew ? "Buat Konfigurasi Model Baru" : `Edit: ${profile?.name}`}
            {profile?.is_default && (
              <Badge variant="outline" className="border-amber-400/50 text-amber-500">
                Sedang Aktif
              </Badge>
            )}
          </SheetTitle>
          <SheetDescription>
            {isNew
              ? 'Setelah dibuat, konfigurasi tidak langsung aktif. Klik "Jadikan Aktif" pada kartu untuk mengaktifkannya.'
              : "Setelah diubah, klik simpan; konfigurasi aktif yang disimpan langsung berlaku untuk semua Agent."}
          </SheetDescription>
        </SheetHeader>

        <div className="flex min-h-0 flex-1 flex-col gap-4 overflow-y-auto px-4 pb-4">
          <div className="grid gap-4 sm:grid-cols-2">
            <div className="grid gap-2">
              <Label htmlFor="p-name">Nama</Label>
              <Input
                id="p-name"
                placeholder="Contoh: OpenAI Produksi"
                value={name}
                onChange={(e) => setName(e.target.value)}
              />
            </div>
            <div className="grid gap-2">
              <Label>Format</Label>
              <Select value={format} onValueChange={(v) => setFormat(v as "anthropic" | "openai" | "openai-responses")}>
                <SelectTrigger>
                  <SelectValue placeholder="Pilih format" />
                </SelectTrigger>
                <SelectContent>
                  <SelectItem value="anthropic">Anthropic</SelectItem>
                  <SelectItem value="openai">OpenAI (Chat Completions)</SelectItem>
                  <SelectItem value="openai-responses">OpenAI (Responses API)</SelectItem>
                </SelectContent>
              </Select>
            </div>
          </div>

          <div className="grid gap-2">
            <Label htmlFor="p-model">Model</Label>
            <div className="flex gap-2">
              <Input
                id="p-model"
                className="font-mono"
                placeholder="claude-opus-4-8"
                value={model}
                onChange={(e) => setModel(e.target.value)}
              />
              {/* modal: konten Popover ini di-portal ke <body>, di luar scroll lock milik Sheet.
                  Tanpa modal, listnya bisa dirender tapi tidak bisa di-scroll. modal membuatnya memegang scroll lock paling atas sendiri. */}
              <Popover open={modelsOpen} onOpenChange={setModelsOpen} modal>
                <PopoverTrigger asChild>
                  <Button
                    type="button"
                    variant="outline"
                    size="icon"
                    className="shrink-0"
                    disabled={loadingModels}
                    onClick={loadModels}
                    title="Muat model yang tersedia dari API"
                  >
                    {loadingModels ? <Loader2Icon className="animate-spin" /> : <RefreshCwIcon />}
                  </Button>
                </PopoverTrigger>
                {models.length > 0 && (
                  <PopoverContent className="max-h-72 w-72 gap-0 overflow-y-auto overscroll-contain p-1" align="end">
                    {models.map((m) => (
                      <button
                        key={m}
                        type="button"
                        className="w-full shrink-0 rounded-md px-2 py-1.5 text-left font-mono text-xs hover:bg-accent hover:text-accent-foreground"
                        onClick={() => {
                          setModel(m);
                          setModelsOpen(false);
                        }}
                      >
                        {m}
                      </button>
                    ))}
                  </PopoverContent>
                )}
              </Popover>
            </div>
          </div>

          <div className="grid gap-2">
            <Label htmlFor="p-base-url">Base URL (opsional)</Label>
            <Input
              id="p-base-url"
              className="font-mono"
              placeholder="https://api.openai.com/v1"
              value={baseUrl}
              onChange={(e) => setBaseUrl(e.target.value)}
            />
          </div>

          <div className="grid gap-2">
            <Label htmlFor="p-proxy">Proxy (opsional)</Label>
            <Input
              id="p-proxy"
              className="font-mono"
              placeholder="socks5://user:pass@127.0.0.1:1080 · http://127.0.0.1:8080"
              value={proxy}
              onChange={(e) => setProxy(e.target.value)}
            />
            <p className="text-muted-foreground text-xs">
              Hanya request keluar ke LLM yang lewat proxy ini, mendukung http/https/socks5, bisa menyertakan akun dan
              kata sandi (contoh socks5://user:pass@host:port, kata sandi yang mengandung karakter khusus perlu
              di-encode URL); dikosongkan berarti tidak memakai proxy (koneksi langsung).
            </p>
          </div>

          <div className="grid gap-2">
            <Label htmlFor="p-session-header">Header Sesi Kustom (opsional)</Label>
            <Input
              id="p-session-header"
              className="font-mono"
              placeholder="Contoh x-session-id (kosongkan = tidak dikirim)"
              value={sessionHeaderKey}
              onChange={(e) => setSessionHeaderKey(e.target.value)}
            />
            <p className="text-muted-foreground text-xs">
              Setelah nama header diisi, setiap request akan menyertakan header HTTP ini, nilainya otomatis diisi
              dengan <b>session id sesi saat ini</b> (sesi chat misalnya conv-12, worker misalnya exp3-worker-i87).
              Berguna untuk gateway yang melakukan prompt caching / sticky routing berdasarkan header session-id;
              stabil untuk banyak putaran dalam sesi yang sama, dan berbeda antar sesi. Jika dikosongkan, tidak dikirim.
            </p>
          </div>

          <div className="grid gap-2">
            <Label htmlFor="p-api-key">API Key</Label>
            <Input
              id="p-api-key"
              type="password"
              placeholder={keyHint ? `Sudah diatur (${keyHint}), kosongkan untuk tidak mengubah` : "sk-…"}
              value={apiKey}
              onChange={(e) => setApiKey(e.target.value)}
            />
          </div>

          <div className="grid gap-4 sm:grid-cols-3">
            <div className="grid gap-2">
              <Label htmlFor="p-rps">Batas Kecepatan per Detik</Label>
              <Input id="p-rps" type="number" min={0} value={rps} onChange={(e) => setRps(e.target.value)} />
            </div>
            <div className="grid gap-2">
              <Label htmlFor="p-rpm">Batas Kecepatan per Menit</Label>
              <Input id="p-rpm" type="number" min={0} value={rpm} onChange={(e) => setRpm(e.target.value)} />
            </div>
            <div className="grid gap-2">
              <Label htmlFor="p-cw">Context Window (K)</Label>
              <Input
                id="p-cw"
                type="number"
                min={0}
                max={1000}
                value={cw}
                onChange={(e) => setCw(e.target.value)}
                placeholder="200"
              />
            </div>
          </div>
          <p className="-mt-2 text-muted-foreground text-xs">
            Batas kecepatan 0 = tidak dibatasi, dibagi bersama semua Agent. Satuan context window adalah K (ribu
            token), 0 = default 200K, maksimum 1000 (yaitu 1M); kalau diset terlalu tinggi, kompresi tidak akan terpicu.
          </p>

          <div className="grid gap-3 rounded-lg border p-3">
            <div className="flex items-center justify-between gap-4">
              <div className="grid gap-0.5">
                <Label htmlFor="p-priority" className="text-sm">
                  Prioritas Rotasi
                </Label>
                <p className="text-muted-foreground text-xs">
                  Angka lebih besar berarti lebih diprioritaskan; konfigurasi aktif selalu berada di urutan pertama,
                  tidak terpengaruh nilai ini. Konfigurasi dengan prioritas sama akan bergiliran di depan, sehingga
                  kuota terbagi secara alami.
                </p>
              </div>
              <Input
                id="p-priority"
                type="number"
                className="w-24 shrink-0"
                value={priority}
                onChange={(e) => setPriority(e.target.value)}
              />
            </div>
            <div className="flex items-center justify-between gap-4 border-t pt-3">
              <div className="grid gap-0.5">
                <Label className="text-sm">Tidak Ikut Rotasi</Label>
                <p className="text-muted-foreground text-xs">
                  Jika diaktifkan, konfigurasi ini tidak akan dijadikan target failover (tetap bisa dipakai jika
                  ditentukan secara eksplisit oleh Agent / tugas). Cocok untuk konfigurasi mahal yang "khusus untuk
                  satu Agent tertentu, tidak ingin terpakai saat yang lain gagal".
                </p>
              </div>
              <Switch checked={poolExclude} onCheckedChange={setPoolExclude} aria-label="Tidak Ikut Rotasi" />
            </div>
            <div className="flex items-center justify-between gap-4 border-t pt-3">
              <div className="grid gap-0.5">
                <Label className="text-sm">Output Streaming · streaming</Label>
                <p className="text-muted-foreground text-xs">
                  Jika diaktifkan (default), memakai SSE streaming, ada progres real-time dan hitungan token real-time
                  saat berjalan. Jika dinonaktifkan, memakai mode benar-benar non-streaming (stream:false, respons
                  lengkap dikembalikan sekaligus), ini bisa menghindari implementasi SSE sejumlah gateway yang buruk
                  (frame kosong / frame field thinking hilang), tapi mengorbankan progres real-time saat berjalan.
                </p>
              </div>
              <Switch checked={streaming} onCheckedChange={setStreaming} aria-label="Output Streaming" />
            </div>
          </div>

          <div className="grid gap-3 rounded-lg border p-3">
            <div className="flex items-center justify-between gap-4">
              <div className="grid gap-0.5">
                <Label htmlFor="p-max-tokens" className="text-sm">
                  Batas Output · max tokens
                </Label>
                <p className="text-muted-foreground text-xs">
                  Jumlah token maksimum yang dihasilkan dalam satu balasan, dikirim di setiap request. 0 (default) =
                  field ini tidak dikirim, nilai default ditentukan oleh server. Ini berbeda dengan "context window"
                  di atas, yang merupakan kapasitas total model dan hanya dipakai secara lokal untuk menghitung
                  ambang kompresi. Jika diset terlalu kecil, model reasoning bisa terpotong di tahap berpikir dan
                  tidak menghasilkan jawaban sama sekali.
                </p>
              </div>
              <Input
                id="p-max-tokens"
                type="number"
                min={0}
                className="w-28 shrink-0"
                value={maxTokens}
                onChange={(e) => setMaxTokens(e.target.value)}
                placeholder="0"
              />
            </div>
            <div className="flex items-center justify-between gap-4 border-t pt-3">
              <div className="grid gap-0.5">
                <Label className="text-sm">Nama Field Batas</Label>
                <p className="text-muted-foreground text-xs">{MAX_TOKENS_FIELD_HINTS[format]}</p>
              </div>
              <Select
                value={format === "openai" ? maxTokensField : NONE}
                onValueChange={setMaxTokensField}
                disabled={format !== "openai"}
              >
                <SelectTrigger className="w-56 shrink-0">
                  <SelectValue />
                </SelectTrigger>
                <SelectContent>
                  {MAX_TOKENS_FIELDS.map((o) => (
                    <SelectItem key={o.value} value={o.value}>
                      {o.label}
                    </SelectItem>
                  ))}
                </SelectContent>
              </Select>
            </div>
          </div>

          <div className="grid gap-3 rounded-lg border p-3">
            <div className="flex items-center justify-between gap-4">
              <div className="grid gap-0.5">
                <Label className="text-sm">Sakelar Thinking · thinking.type</Label>
                <p className="text-muted-foreground text-xs">
                  Mengatur apakah field thinking dikirim. Tidak dikirim = tanpa field ini (kompatibel dengan model
                  seperti MiniMax yang tidak mendukungnya); Nonaktif = mengirim disabled; Aktif = mengirim enabled.
                  Independen dari intensitas di bawah.
                </p>
              </div>
              <Select value={thinkingType} onValueChange={setThinkingType}>
                <SelectTrigger className="w-32 shrink-0">
                  <SelectValue />
                </SelectTrigger>
                <SelectContent>
                  {THINKING_TYPES.map((o) => (
                    <SelectItem key={o.value} value={o.value}>
                      {o.label}
                    </SelectItem>
                  ))}
                </SelectContent>
              </Select>
            </div>
            <div className="flex items-center justify-between gap-4 border-t pt-3">
              <div className="grid gap-0.5">
                <Label className="text-sm">Intensitas Thinking · reasoning_effort</Label>
                <p className="text-muted-foreground text-xs">
                  Tingkat intensitas yang independen (OpenAI reasoning_effort / Anthropic output_config.effort).
                  Beberapa API tidak punya field thinking dan cukup mengandalkan intensitas untuk mengaktifkan
                  thinking, jadi bisa diatur sendiri tanpa mengirim sakelar thinking.
                </p>
              </div>
              <Select value={effort} onValueChange={setEffort}>
                <SelectTrigger className="w-32 shrink-0">
                  <SelectValue />
                </SelectTrigger>
                <SelectContent>
                  {EFFORT_LEVELS.map((o) => (
                    <SelectItem key={o.value} value={o.value}>
                      {o.label}
                    </SelectItem>
                  ))}
                </SelectContent>
              </Select>
            </div>
          </div>

          <ProfileRetryFields value={retry} onChange={setRetry} />
        </div>

        <div className="flex gap-2 border-t px-4 py-3">
          <Button variant="outline" onClick={testConnection} disabled={testing}>
            {testing ? <Loader2Icon className="animate-spin" /> : <PlugZapIcon />}
            {testing ? "Menguji..." : "Uji Koneksi"}
          </Button>
          <Button onClick={save} disabled={saving} className="flex-1">
            {saving && <Loader2Icon className="animate-spin" />}
            {!saving && (isNew ? <PlusIcon /> : <SaveIcon />)}
            {isNew ? "Buat" : "Simpan"}
          </Button>
        </div>
      </SheetContent>
    </Sheet>
  );
}

// ─────────────────────────────────────────────────────────────────────────────

export default function LLMPage() {
  const [profiles, setProfiles] = React.useState<LLMProfile[]>([]);
  const [pool, setPool] = React.useState<LLMPoolStatus | null>(null);
  const [poolOpen, setPoolOpen] = React.useState(false);
  // Status buka/tutup drawer dan isinya disimpan terpisah: saat ditutup, editing tetap tidak berubah, kalau
  // tidak judulnya akan berkedip dari "Edit X" menjadi "Buat Baru" selama animasi penutupan. editing = null berarti buat baru.
  const [editOpen, setEditOpen] = React.useState(false);
  const [editing, setEditing] = React.useState<LLMProfile | null>(null);
  const openEditor = React.useCallback((p: LLMProfile | null) => {
    setEditing(p);
    setEditOpen(true);
  }, []);

  const loadPool = React.useCallback(async () => {
    try {
      setPool(await api.llmPool());
    } catch {
      /* ignore */
    }
  }, []);

  const load = React.useCallback(async () => {
    try {
      setProfiles(await api.llmProfiles());
    } catch {
      /* ignore */
    }
    await loadPool();
  }, [loadPool]);

  React.useEffect(() => {
    void load();
  }, [load]);

  // Badge kesehatan pada kartu mengambil status rotasi berdasarkan profile id.
  const health = React.useMemo(() => {
    const m = new Map<string, LLMPoolMember>();
    for (const c of pool?.chain ?? []) m.set(c.profile_id, c);
    return m;
  }, [pool]);

  async function activate(id: string, name: string) {
    try {
      await api.activateLLMProfile(id);
      toast.success(`Berhasil diaktifkan: ${name}`);
      await load();
    } catch (e) {
      toast.error(`Gagal mengaktifkan: ${(e as Error).message}`);
    }
  }

  async function remove(p: LLMProfile) {
    if (p.is_default) {
      toast.error("Tidak bisa menghapus konfigurasi yang sedang aktif");
      return;
    }
    try {
      await api.deleteLLMProfile(p.id);
      toast.success(`Berhasil dihapus: ${p.name}`);
      await load();
    } catch (e) {
      toast.error(`Gagal menghapus: ${(e as Error).message}`);
    }
  }

  const poolOn = pool?.enabled ?? false;

  return (
    <div className="flex flex-1 flex-col gap-4 md:gap-6">
      <div className="flex flex-wrap items-start justify-between gap-3">
        <div>
          <h1 className="font-semibold text-xl tracking-tight">LLM</h1>
          <p className="text-muted-foreground text-sm">
            Konfigurasi format / model / batas kecepatan yang dibagikan ke semua Agent. Klik kartu untuk mengedit,
            bintang menandai konfigurasi yang sedang aktif.
          </p>
        </div>
        <div className="flex items-center gap-2">
          <Button size="sm" variant="outline" onClick={() => setPoolOpen(true)}>
            <ZapIcon /> Konfigurasi Rotasi
            {poolOn && (
              <Badge variant="outline" className="ml-1 border-emerald-500/50 text-emerald-600 dark:text-emerald-400">
                Aktif
              </Badge>
            )}
          </Button>
          <Button size="sm" variant="outline" onClick={() => openEditor(null)}>
            <PlusIcon /> Buat Baru
          </Button>
        </div>
      </div>

      <Tabs defaultValue="profiles" className="flex-1">
        <TabsList>
          <TabsTrigger value="profiles">Konfigurasi Model</TabsTrigger>
          <TabsTrigger value="retry">Retry dan Backoff</TabsTrigger>
        </TabsList>

        <TabsContent value="profiles" className="mt-4">
          <div className="grid grid-cols-1 gap-4 md:grid-cols-2 xl:grid-cols-3">
            {profiles.map((p) => {
              const h = healthOf(p, health.get(p.id));
              return (
                // biome-ignore lint/a11y/useSemanticElements: Kartu ini memiliki tombol aksinya sendiri, memakai <button> native akan menyebabkan tombol bersarang (HTML tidak valid)
                <Card
                  key={p.id}
                  role="button"
                  tabIndex={0}
                  onClick={() => openEditor(p)}
                  onKeyDown={(e) => {
                    if (e.key === "Enter" || e.key === " ") {
                      e.preventDefault();
                      openEditor(p);
                    }
                  }}
                  className={cn(
                    "cursor-pointer gap-0 py-4 outline-none transition-colors hover:border-foreground/30",
                    p.is_default && "border-amber-400/50 bg-amber-400/5",
                  )}
                >
                  <CardContent className="grid gap-2 px-4">
                    <div className="flex items-start gap-2">
                      <StarIcon
                        className={cn(
                          "mt-0.5 size-4 shrink-0",
                          p.is_default ? "fill-amber-400 text-amber-400" : "text-muted-foreground",
                        )}
                      />
                      <div className="min-w-0 flex-1">
                        <div className="flex flex-wrap items-center gap-2">
                          <span className="truncate font-medium text-sm">{p.name}</span>
                          <Badge variant="outline" className="uppercase">
                            {p.format}
                          </Badge>
                          <Badge variant="outline" className={cn("ml-auto", h.cls)} title={h.hint}>
                            {h.label}
                          </Badge>
                        </div>
                        <code className="mt-1 block truncate font-mono text-muted-foreground text-xs">{p.model}</code>
                      </div>
                    </div>

                    <div className="flex flex-wrap gap-x-3 gap-y-0.5 pl-6 text-muted-foreground text-xs">
                      {p.api_key_hint && <span>{p.api_key_hint}</span>}
                      <span>
                        {p.rate_per_second}/s · {p.rate_per_minute}/min
                      </span>
                      {p.proxy && <span className="truncate">Proxy {p.proxy}</span>}
                      {p.reasoning_effort && (
                        <span>Thinking {p.reasoning_effort === "off" ? "Nonaktif" : p.reasoning_effort}</span>
                      )}
                      {/* Dua field terkait rotasi hanya relevan saat rotasi aktif, saat nonaktif tidak memakan tempat */}
                      {poolOn &&
                        !p.is_default &&
                        (p.pool_exclude ? <span>Tidak Ikut Rotasi</span> : <span>Prioritas {p.priority ?? 0}</span>)}
                    </div>

                    <div className="mt-1 flex gap-2">
                      <Button
                        size="sm"
                        variant="outline"
                        className="flex-1"
                        disabled={p.is_default}
                        onClick={(e) => {
                          e.stopPropagation();
                          void activate(p.id, p.name);
                        }}
                      >
                        {p.is_default ? "Sudah Aktif" : "Jadikan Aktif"}
                      </Button>
                      <Button
                        size="icon"
                        variant="outline"
                        aria-label="Hapus Konfigurasi"
                        onClick={(e) => {
                          e.stopPropagation();
                          void remove(p);
                        }}
                      >
                        <Trash2Icon className="text-destructive" />
                      </Button>
                    </div>
                  </CardContent>
                </Card>
              );
            })}
            {profiles.length === 0 && (
              <div className="col-span-full rounded-lg border border-dashed p-10 text-center text-muted-foreground text-sm">
                Belum ada konfigurasi model, klik "Buat Baru" di kanan atas untuk membuat yang pertama.
              </div>
            )}
          </div>
        </TabsContent>

        <TabsContent value="retry" className="mt-4">
          <RetryPolicyPanel />
        </TabsContent>
      </Tabs>

      <ProfileSheet profile={editing} open={editOpen} onOpenChange={setEditOpen} onSaved={() => void load()} />
      <PoolSheet open={poolOpen} onOpenChange={setPoolOpen} pool={pool} onReload={loadPool} />
    </div>
  );
}
