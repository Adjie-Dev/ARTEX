"use client";

// Komponen bersama untuk konfigurasi retry LLM: "jumlah percobaan + interval" masing-masing dari lima lapisan retry.
//
// Lima lapisan dari dalam ke luar: koneksi (SDK) → respons kosong (SDK) → safety window provider yang sama → circuit breaker polling → rerun intent.
// Tiga lapisan pertama mengikuti endpoint, jadi setiap konfigurasi model bisa mengoverride default global; dua lapisan terakhir bersifat level proses, hanya ada satu untuk global.
//
// Semua input mengikuti semantik "dikosongkan = tidak dikonfigurasi" yang sama, selaras dengan db.RetryRule di backend:
//   Jumlah percobaan  kosong/0 = pakai default bawaan | -1 = matikan retry lapisan ini | >0 = pakai jumlah ini
//   Interval          kosong/0 = pakai exponential backoff asli lapisan ini | >0 = pakai interval milidetik fixed ini

import * as React from "react";

import { Loader2Icon, SaveIcon } from "lucide-react";
import { toast } from "sonner";

import { Button } from "@/components/ui/button";
import { Input } from "@/components/ui/input";
import { Label } from "@/components/ui/label";
import { api } from "@/lib/api";
import type { LLMRetryOverride, LLMRetryPolicy, LLMRetryRule } from "@/lib/types";

export const ZERO_RULE: LLMRetryRule = { attempts: 0, interval_ms: 0 };
export const ZERO_OVERRIDE: LLMRetryOverride = {
  connect: ZERO_RULE,
  empty: ZERO_RULE,
  stream: ZERO_RULE,
};
const ZERO_POLICY: LLMRetryPolicy = {
  ...ZERO_OVERRIDE,
  breaker: ZERO_RULE,
  intent: ZERO_RULE,
};

type LayerMeta = {
  title: string;
  /** Di mana lapisan retry ini terjadi dan siapa yang mengeksekusinya */
  where: string;
  /** Jenis error apa yang akan masuk ke lapisan ini, spesifik sampai status code, supaya tidak menebak-nebak */
  trigger: string;
  /** Error yang terlihat mirip tapi TIDAK masuk ke lapisan ini, supaya tidak mengisi lalu mengira tidak ada efek adalah bug */
  skips?: string;
  desc: string;
  attemptsLabel: string;
  /** Nilai default saat jumlah percobaan dikosongkan, dipakai untuk placeholder */
  defAttempts: number;
  /** Kebijakan default saat interval dikosongkan, dipakai untuk placeholder */
  defInterval: string;
  /** Arti jika jumlah percobaan diisi -1 */
  offHint: string;
};

export const RETRY_LAYERS = {
  connect: {
    title: "Retry koneksi",
    where: "SDK, sebelum mendapat 200",
    trigger:
      "Tidak bisa konek atau belum mendapat 200: error level jaringan seperti connection reset / timeout baca-tulis / DNS gagal, serta HTTP 408, 429, 500, 502, 503, 504.",
    skips: "Status code lainnya (400 / 401 / 403 / 404 / 413 / 422 dan sejenisnya) adalah penolakan deterministik, dikirim ulang pun tetap gagal, jadi langsung dilempar ke atas.",
    desc: "Mengirim ulang request yang sama persis. Begitu stream dimulai (sudah mendapat 200), putus di tengah jalan bukan tanggung jawab lapisan ini lagi.",
    attemptsLabel: "Jumlah percobaan",
    defAttempts: 3,
    defInterval: "0.5s→1s→2s eksponensial (maksimum 8s)",
    offHint: "-1 = tidak mencoba ulang sama sekali, gagal langsung dilempar ke atas",
  },
  empty: {
    title: "Retry respons kosong",
    where: "SDK, hanya format openai",
    trigger:
      "HTTP 200, finish_reason bernilai stop normal, tapi seluruh respons tidak punya satu pun content block. Frame kosong dari gateway, frame hilang pada field thinking, atau gangguan sampling bisa terlihat seperti ini.",
    skips: "Tidak termasuk kasus terpotong karena max_tokens (itu harus diatasi dengan menaikkan batas output, kirim ulang hanya akan mengulang masalah yang sama).",
    desc: "Mengirim ulang seluruh prompt, jadi cukup mahal pada konteks panjang, jumlah percobaan sebaiknya tidak diset terlalu besar.",
    attemptsLabel: "Jumlah percobaan",
    defAttempts: 2,
    defInterval: "0.5s→1s→2s eksponensial (maksimum 8s)",
    offHint: "-1 = respons kosong langsung diteruskan apa adanya",
  },
  stream: {
    title: "Retry safety window provider yang sama",
    where: "Proyek ini, sebelum output diserahkan",
    trigger:
      "Masalah terjadi setelah stream terbentuk (sudah mendapat 200): koneksi putus di tengah jalan, provider overloaded, event error 429 / 5xx di dalam stream, dan belum ada satu token pun yang diserahkan ke pemanggil.",
    skips:
      "Kuota habis (402 / insufficient_quota, diserahkan ke polling untuk ganti konfigurasi), konteks terlalu panjang (413 / context length, diserahkan ke kompresi), serta penolakan deterministik 400 / 401 / 403 / 404 / 422, semuanya tidak dicoba ulang.",
    desc: "Memutar ulang request yang sama pada konfigurasi yang sama. Karena belum ada output yang diserahkan, pemutaran ulang tidak akan menduplikasi output model atau eksekusi tool.",
    attemptsLabel: "Jumlah percobaan",
    defAttempts: 2,
    defInterval: "0.5s→1s eksponensial (maksimum 4s)",
    offHint: "-1 = stream putus langsung diserahkan ke rerun intent di lapisan luar",
  },
  breaker: {
    title: "Circuit breaker polling",
    where: "Proyek ini, level proses, satu untuk global",
    trigger:
      "Circuit breaker aktif saat kegagalan sesaat (429, 5xx, error jaringan) terakumulasi berturut-turut sampai ambang batas; kegagalan deterministik seperti saldo tidak cukup (402), kunci tidak valid (401 / 403), model tidak ada (404) tidak melihat ambang batas, langsung breaker begitu terjadi pertama kali.",
    skips: "Satu kali sukses langsung mereset hitungan ke nol, jadi konfigurasi yang sesekali bermasalah tidak akan pelan-pelan terakumulasi sampai breaker.",
    desc: "Setelah breaker aktif, masuk masa cooldown, selama cooldown polling langsung melewati konfigurasi ini. Statusnya disimpan ke database, tidak hilang saat restart.",
    attemptsLabel: "Berapa kali gagal berturut-turut sampai breaker aktif",
    defAttempts: 3,
    defInterval: "1min→5min→30min bertingkat",
    offHint: "-1 = kegagalan sesaat tidak akan pernah memicu breaker (kegagalan deterministik tetap memicu breaker)",
  },
  intent: {
    title: "Rerun intent",
    where: "Proyek ini, level proses, satu untuk global",
    trigger:
      "Semua lapisan sebelumnya tidak berhasil menangkap masalah: worker berakhir dengan model_error, baik karena retry di lapisan dalam sudah habis semua, atau karena stream putus setelah output mulai diserahkan (saat itu pemutaran ulang tidak aman, satu-satunya cara adalah mengulang dari awal).",
    skips: "Kuota habis sudah ditangani oleh polling yang mengganti konfigurasi, tidak di-rerun di sini; saat tugas dijeda / dihentikan / memasuki penutupan, lapisan ini langsung mengalah dan tidak memakai waktu backoff.",
    desc: "Menjalankan ulang seluruh intent dari awal. Ini adalah lapisan paling luar, satu kali rerun berarti jumlah percobaan lapisan-lapisan di dalamnya akan dikalikan lagi.",
    attemptsLabel: "Jumlah rerun",
    defAttempts: 2,
    defInterval: "Fixed 3s",
    offHint: "-1 = tidak di-rerun, intent tersebut langsung dianggap blocked",
  },
} satisfies Record<string, LayerMeta>;

type LayerKey = keyof typeof RETRY_LAYERS;

/** Versi mudah dibaca dari milidetik, hanya untuk ditampilkan di samping input, supaya tidak perlu menghitung angka nol. */
function humanMs(ms: number) {
  if (!Number.isFinite(ms) || ms <= 0) return "";
  if (ms < 1000) return `${ms}ms`;
  if (ms < 60_000) return `${Number((ms / 1000).toFixed(2))}s`;
  return `${Number((ms / 60_000).toFixed(2))}min`;
}

/** Input angka terkontrol: string kosong setara 0, state sementara ("-", "1e") dibiarkan apa adanya secara lokal, tidak mengganggu parent. */
function NumField({
  id,
  value,
  onChange,
  placeholder,
  min,
}: {
  id: string;
  value: number;
  onChange: (n: number) => void;
  placeholder: string;
  min: number;
}) {
  const [text, setText] = React.useState(value === 0 ? "" : String(value));
  // Mengikuti saat parent mengganti seluruh set nilai (saat memuat policy, berganti konfigurasi); saat mengetik sendiri tidak
  // akan masuk ke sini, karena saat itu value sudah sama dengan hasil parse dari teks lokal.
  React.useEffect(() => {
    const incoming = value === 0 ? "" : String(value);
    setText((cur) => (Number(cur || 0) === value ? cur : incoming));
  }, [value]);
  return (
    <Input
      id={id}
      type="number"
      min={min}
      className="w-28 shrink-0"
      value={text}
      placeholder={placeholder}
      onChange={(e) => {
        setText(e.target.value);
        const n = Number(e.target.value);
        onChange(e.target.value.trim() === "" || !Number.isFinite(n) ? 0 : Math.trunc(n));
      }}
    />
  );
}

/** Dua kontrol untuk satu lapisan retry. idPrefix dipakai agar htmlFor label tetap unik saat muncul beberapa kali di halaman yang sama. */
export function RetryRuleFields({
  layer,
  idPrefix,
  value,
  onChange,
  compact,
}: {
  layer: LayerKey;
  idPrefix: string;
  value: LLMRetryRule;
  onChange: (r: LLMRetryRule) => void;
  /** true = versi ringkas di panel konfigurasi: menghilangkan penjelasan lengkap, hanya menyisakan kalimat "error apa yang masuk ke lapisan ini" */
  compact?: boolean;
}) {
  const meta = RETRY_LAYERS[layer];
  const human = humanMs(value.interval_ms);
  return (
    <div className={compact ? "grid gap-2" : "grid gap-3 rounded-lg border p-3"}>
      <div className="grid gap-0.5">
        <div className="flex flex-wrap items-baseline gap-2">
          <Label className="text-sm">{meta.title}</Label>
          <span className="text-muted-foreground text-xs">{meta.where}</span>
        </div>
        {/* Error mana yang masuk ke lapisan ini, spesifik sampai status code. Jika sudah diisi tapi tidak terlihat efeknya, kemungkinan besar error tersebut memang tidak jatuh ke lapisan ini. */}
        <p className="text-muted-foreground text-xs">
          <span className="font-medium text-foreground">Pemicu</span>: {meta.trigger}
        </p>
        {!compact && meta.skips && (
          <p className="text-muted-foreground text-xs">
            <span className="font-medium text-foreground">Tidak masuk ke lapisan ini</span>: {meta.skips}
          </p>
        )}
        {!compact && <p className="text-muted-foreground text-xs">{meta.desc}</p>}
      </div>
      <div className="flex flex-wrap items-center gap-x-4 gap-y-2">
        <div className="flex items-center gap-2">
          <Label htmlFor={`${idPrefix}-${layer}-n`} className="text-muted-foreground text-xs">
            {meta.attemptsLabel}
          </Label>
          <NumField
            id={`${idPrefix}-${layer}-n`}
            min={-1}
            value={value.attempts}
            placeholder={`Default ${meta.defAttempts}`}
            onChange={(n) => onChange({ ...value, attempts: n })}
          />
        </div>
        <div className="flex items-center gap-2">
          <Label htmlFor={`${idPrefix}-${layer}-ms`} className="text-muted-foreground text-xs">
            Interval ms
          </Label>
          <NumField
            id={`${idPrefix}-${layer}-ms`}
            min={0}
            value={value.interval_ms}
            placeholder="Default backoff"
            onChange={(n) => onChange({ ...value, interval_ms: n })}
          />
          <span className="text-muted-foreground text-xs">{human ? `Fixed ${human}` : meta.defInterval}</span>
        </div>
      </div>
      {!compact && <p className="text-muted-foreground text-xs">Dikosongkan = pakai default; {meta.offHint}.</p>}
    </div>
  );
}

/** Tiga lapisan override di panel konfigurasi model (tiga lapisan yang mengikuti endpoint). */
export function ProfileRetryFields({
  value,
  onChange,
}: {
  value: LLMRetryOverride;
  onChange: (o: LLMRetryOverride) => void;
}) {
  return (
    <div className="grid gap-3 rounded-lg border p-3">
      <div className="grid gap-0.5">
        <Label className="text-sm">Retry Override</Label>
        <p className="text-muted-foreground text-xs">
          Hanya berlaku untuk konfigurasi ini, mengoverride default global di "Retry & Backoff". Setiap kolom dikosongkan
          = ikut global; jumlah percobaan diisi -1 = matikan retry lapisan ini; interval diisi akan memakai interval fixed
          menggantikan exponential backoff. Circuit breaker dan rerun intent bersifat level proses, hanya bisa diatur di halaman global.
        </p>
      </div>
      {(["connect", "empty", "stream"] as const).map((k) => (
        <div key={k} className="border-t pt-3 first:border-t-0 first:pt-0">
          <RetryRuleFields
            compact
            layer={k}
            idPrefix="pf"
            value={value[k]}
            onChange={(r) => onChange({ ...value, [k]: r })}
          />
        </div>
      ))}
    </div>
  );
}

/** Tab "Retry & Backoff": nilai default global dari lima lapisan. */
export function RetryPolicyPanel() {
  const [policy, setPolicy] = React.useState<LLMRetryPolicy>(ZERO_POLICY);
  const [loading, setLoading] = React.useState(true);
  const [saving, setSaving] = React.useState(false);

  const load = React.useCallback(async () => {
    setLoading(true);
    try {
      const p = await api.llmRetryPolicy();
      setPolicy({ ...ZERO_POLICY, ...p });
    } catch (e) {
      toast.error(`Gagal membaca kebijakan retry: ${(e as Error).message}`);
    } finally {
      setLoading(false);
    }
  }, []);

  React.useEffect(() => {
    void load();
  }, [load]);

  async function save() {
    if (saving) return;
    setSaving(true);
    try {
      // Backend akan membatasi nilai yang melewati rentang lalu mengirimkannya kembali, langsung pakai nilai balikan untuk refresh, apa yang terlihat adalah yang tersimpan.
      const saved = await api.saveLLMRetryPolicy(policy);
      setPolicy({ ...ZERO_POLICY, ...saved });
      toast.success("Tersimpan, langsung berlaku (panggilan yang sedang berjalan saat ini masih memakai parameter lama)");
    } catch (e) {
      toast.error(`Gagal menyimpan: ${(e as Error).message}`);
    } finally {
      setSaving(false);
    }
  }

  const set = (k: LayerKey) => (r: LLMRetryRule) => setPolicy((p) => ({ ...p, [k]: r }));

  if (loading) {
    return (
      <div className="flex items-center gap-2 rounded-lg border border-dashed p-10 text-muted-foreground text-sm">
        <Loader2Icon className="size-4 animate-spin" /> Membaca kebijakan retry...
      </div>
    );
  }

  return (
    <div className="grid gap-4">
      <div className="rounded-lg border bg-muted/30 p-3 text-muted-foreground text-xs leading-relaxed">
        Kegagalan satu kali panggilan model akan melewati lima lapisan retry secara berurutan, dari dalam ke luar:
        <span className="text-foreground"> koneksi → respons kosong → safety window provider yang sama → circuit breaker polling → rerun intent</span>
        . Lapisan dalam harus habis dulu baru giliran lapisan luar, jadi jumlah percobaannya bersifat
        <span className="text-foreground">berkali lipat</span>
        , jika setiap lapisan diisi penuh, satu kali gangguan bisa menghabiskan puluhan request.
        Semua dikosongkan berarti memakai nilai default saat ini, perilakunya sama persis seperti saat halaman ini belum ada. Tiga lapisan pertama bisa dioverride sendiri-sendiri di setiap konfigurasi model.
      </div>

      <div className="grid gap-3 md:grid-cols-2 xl:grid-cols-3">
        {(Object.keys(RETRY_LAYERS) as LayerKey[]).map((k) => (
          <RetryRuleFields key={k} layer={k} idPrefix="gl" value={policy[k]} onChange={set(k)} />
        ))}
      </div>

      <div className="flex gap-2">
        <Button onClick={save} disabled={saving}>
          {saving ? <Loader2Icon className="animate-spin" /> : <SaveIcon />}
          Simpan
        </Button>
        <Button variant="outline" onClick={() => setPolicy(ZERO_POLICY)} disabled={saving}>
          Kembalikan semua ke default
        </Button>
      </div>
    </div>
  );
}
