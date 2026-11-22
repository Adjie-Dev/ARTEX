// Centralised status → color/label semantics, reused across the whole app.
// Spec §8.3: intent / cakupan / Tugas / severity masing-masing punya set warna yang konsisten.

export type Tone = "neutral" | "blue" | "green" | "amber" | "red" | "rose" | "violet" | "slate";

export const toneClasses: Record<Tone, string> = {
  neutral: "bg-muted text-muted-foreground border-transparent",
  blue: "bg-blue-500/15 text-blue-600 dark:text-blue-400 border-blue-500/20",
  green: "bg-emerald-500/15 text-emerald-600 dark:text-emerald-400 border-emerald-500/20",
  amber: "bg-amber-500/15 text-amber-600 dark:text-amber-400 border-amber-500/20",
  red: "bg-red-500/15 text-red-600 dark:text-red-400 border-red-500/20",
  // rose dipakai untuk "Kritis", solid dengan penekanan tinggi, secara visual jelas lebih menonjol daripada garis tepi merah lembut pada "Tinggi".
  rose: "bg-rose-600 text-white border-rose-600 dark:bg-rose-600 dark:text-white",
  violet: "bg-violet-500/15 text-violet-600 dark:text-violet-400 border-violet-500/20",
  slate: "bg-slate-500/15 text-slate-600 dark:text-slate-400 border-slate-500/20",
};

export const toneDot: Record<Tone, string> = {
  neutral: "bg-muted-foreground",
  blue: "bg-blue-500",
  green: "bg-emerald-500",
  amber: "bg-amber-500",
  red: "bg-red-500",
  rose: "bg-white",
  violet: "bg-violet-500",
  slate: "bg-slate-500",
};

interface StatusMeta {
  label: string;
  tone: Tone;
}

const intent: Record<string, StatusMeta> = {
  open: { label: "Menunggu Diambil", tone: "slate" },
  running: { label: "Sedang Berjalan", tone: "blue" },
  paused: { label: "Dijeda", tone: "amber" },
  done: { label: "Selesai", tone: "green" },
  // blocked = percobaan ulang model/API/jaringan sudah habis karena gagal terus-menerus, intent ini pada dasarnya belum benar-benar berhasil dieksplorasi (bukan karena diblokir oleh target).
  blocked: { label: "Error Eksekusi", tone: "red" },
  // exhausted = dihentikan di tengah jalan karena mencapai batas langkah/anggaran waktu, hanya sebagian hasil yang ditulis kembali (bukan berarti arah eksplorasi sudah habis).
  exhausted: { label: "Anggaran Habis", tone: "violet" },
  // stopped = status soft-delete lama (dipertahankan untuk data historis).
  stopped: { label: "Dihentikan", tone: "slate" },
  // deleted = pengguna melakukan soft delete pada intent ini (node dan silsilahnya tetap disimpan, alasan penghapusan ada di field delete_reason).
  deleted: { label: "Dihapus", tone: "slate" },
};

const task: Record<string, StatusMeta> = {
  created: { label: "Dibuat", tone: "slate" },
  queued: { label: "Dalam Antrean", tone: "amber" },
  running: { label: "Sedang Berjalan", tone: "blue" },
  paused: { label: "Dijeda", tone: "amber" },
  done: { label: "Selesai", tone: "green" },
  failed: { label: "Gagal", tone: "red" },
  timeout: { label: "Habis Waktu", tone: "amber" },
};

const severity: Record<string, StatusMeta> = {
  critical: { label: "Kritis", tone: "rose" },
  high: { label: "Tinggi", tone: "red" },
  medium: { label: "Sedang", tone: "amber" },
  low: { label: "Rendah", tone: "slate" },
};

const finding: Record<string, StatusMeta> = {
  pending: { label: "Menunggu", tone: "amber" },
  in_progress: { label: "Diproses", tone: "blue" },
  confirmed: { label: "Terkonfirmasi", tone: "red" },
  resolved: { label: "Sudah Ditangani", tone: "green" },
  fixed: { label: "Sudah Diperbaiki", tone: "green" },
  false_positive: { label: "Positif Palsu", tone: "slate" },
  ignored: { label: "Diabaikan", tone: "neutral" },
  duplicate: { label: "Duplikat", tone: "neutral" },
  risk_accepted: { label: "Risiko Diterima", tone: "violet" },
};

const engine: Record<string, StatusMeta> = {
  exploring: { label: "Sedang Eksplorasi", tone: "blue" },
  paused: { label: "Dijeda", tone: "amber" },
  stalled: { label: "Macet", tone: "red" },
  idle: { label: "Menganggur", tone: "neutral" },
};

const goal: Record<string, StatusMeta> = {
  open: { label: "Sedang Berjalan", tone: "blue" },
  met: { label: "Tercapai", tone: "green" },
  abandoned: { label: "Ditinggalkan", tone: "slate" },
};

const audit: Record<string, StatusMeta> = {
  allow: { label: "Diizinkan", tone: "green" },
  block: { label: "Intersepsi", tone: "red" },
};

const node: Record<string, StatusMeta> = {
  observed: { label: "Teramati", tone: "slate" },
  confirmed: { label: "Dikonfirmasi", tone: "green" },
  tombstoned: { label: "Dibuang", tone: "neutral" },
};

// Status pengiriman notifikasi. sending memakai warna blue, bukan amber:
// bukan berarti "ada masalah", melainkan "sudah diambil dan sedang dikirim", agar bisa dibedakan dari makna menunggu pada pending.
const delivery: Record<string, StatusMeta> = {
  pending: { label: "Menunggu Dikirim", tone: "amber" },
  sending: { label: "Sedang Dikirim", tone: "blue" },
  sent: { label: "Terkirim", tone: "green" },
  failed: { label: "Gagal", tone: "red" },
  skipped: { label: "Dilewati", tone: "neutral" },
};

const maps = {
  intent,
  task,
  severity,
  finding,
  engine,
  goal,
  audit,
  node,
  delivery,
} as const;

export type StatusDomain = keyof typeof maps;

export function statusMeta(domain: StatusDomain, key: string): StatusMeta {
  return maps[domain][key] ?? { label: key, tone: "neutral" };
}
