"use client";

import * as React from "react";

import {
  CheckCircle2Icon,
  DownloadIcon,
  ExternalLinkIcon,
  RefreshCwIcon,
  RotateCcwIcon,
  TriangleAlertIcon,
} from "lucide-react";
import { toast } from "sonner";

import { Badge } from "@/components/ui/badge";
import { Button } from "@/components/ui/button";
import { Card, CardContent, CardDescription, CardHeader, CardTitle } from "@/components/ui/card";
import { Progress } from "@/components/ui/progress";
import { api, sseUrl } from "@/lib/api";
import type { UpdateCheck, UpdateProgress } from "@/lib/types";

/** Waktu maksimum menunggu versi baru online. Satu kali upgrade melewati tiga kali proses restart
 *  (staging → ganti versi → versi baru), masing-masing hanya hitungan detik, tiga menit sudah cukup
 *  untuk menutupi disk lambat dan pembuatan ulang container Docker. */
const RESTART_TIMEOUT_MS = 180_000;

function humanSize(n?: number): string {
  if (!n || n <= 0) return "";
  const units = ["B", "KB", "MB", "GB"];
  let v = n;
  let i = 0;
  while (v >= 1024 && i < units.length - 1) {
    v /= 1024;
    i++;
  }
  return `${v.toFixed(i === 0 ? 0 : 1)} ${units[i]}`;
}

const sleep = (ms: number) => new Promise((r) => setTimeout(r, ms));

export function UpdateCard() {
  const [info, setInfo] = React.useState<UpdateCheck | null>(null);
  const [checking, setChecking] = React.useState(true);
  const [progress, setProgress] = React.useState<UpdateProgress | null>(null);
  // Dipisah dari progress: setelah staging selesai prosesnya sudah hilang, SSE akan putus, saat itu harus beralih ke polling /api/health.
  const [restarting, setRestarting] = React.useState(false);
  const [busy, setBusy] = React.useState(false);

  // quiet juga menentukan apakah melewati cache backend: pengecekan otomatis saat membuka halaman
  // memakai cache (baru saja dicek di top bar), sedangkan klik manual "Cek Pembaruan" memaksa ambil
  // ulang dari sumber, kalau tidak versi yang baru rilis baru terlihat setelah cache kedaluwarsa.
  const check = React.useCallback((quiet = false) => {
    setChecking(true);
    api
      .checkUpdate(!quiet)
      .then((r) => {
        setInfo(r);
        if (!quiet) {
          if (r.error) toast.error("Gagal memeriksa pembaruan: " + r.error);
          else if (r.has_update) toast.success(`Versi baru ditemukan ${r.latest}`);
          else if (r.comparable) toast.success("Sudah versi terbaru");
        }
      })
      .catch((e) => {
        if (!quiet) toast.error("Gagal memeriksa pembaruan: " + (e as Error).message);
      })
      .finally(() => setChecking(false));
  }, []);

  React.useEffect(() => {
    check(true);
  }, [check]);

  // Polling /api/health sampai nomor versi berubah.
  //
  // Kriterianya harus "versinya berubah", bukan "bisa terhubung": selama proses ganti versi, versi
  // lama akan sempat menyala sebentar (hanya untuk mengganti ke artex.new lalu langsung keluar),
  // kalau cuma mengecek konektivitas bisa salah dianggap berhasil.
  const waitForNewVersion = React.useCallback(async (fromVersion: string) => {
    setRestarting(true);
    const deadline = Date.now() + RESTART_TIMEOUT_MS;
    while (Date.now() < deadline) {
      await sleep(2000);
      try {
        const r = await fetch("/api/health", { cache: "no-store" });
        if (r.ok) {
          const j = (await r.json()) as { version?: string };
          if (j.version && j.version !== fromVersion) {
            toast.success(`Berhasil diperbarui ke ${j.version}, memuat ulang halaman`);
            await sleep(800);
            window.location.reload();
            return;
          }
        }
      } catch {
        // Tidak bisa terhubung selama jendela restart memang wajar, lanjutkan polling.
      }
    }
    setRestarting(false);
    toast.error("Waktu tunggu restart layanan habis. Periksa log backend, atau pastikan artex dijalankan lewat start.sh / start.bat.");
  }, []);

  // Berlangganan progres pembaruan. SSE tidak lewat rewrite /api milik Next (lapisan itu akan buffer, event tidak bisa dipush keluar).
  const openStream = React.useCallback(
    (fromVersion: string) => {
      const es = new EventSource(sseUrl("/api/update/stream"));
      es.onmessage = (ev) => {
        let p: UpdateProgress;
        try {
          p = JSON.parse(ev.data) as UpdateProgress;
        } catch {
          return;
        }
        setProgress(p);
        if (p.phase === "failed") {
          es.close();
          setBusy(false);
          toast.error("Pembaruan gagal: " + (p.error || p.message));
          return;
        }
        if (p.phase === "staged") {
          es.close();
          void waitForNewVersion(fromVersion);
        }
      };
      es.onerror = () => {
        // SSE pasti putus saat proses keluar. Kalau sudah masuk fase menunggu restart, ini wajar,
        // biarkan polling /api/health yang menentukan status selanjutnya.
        es.close();
      };
      return es;
    },
    [waitForNewVersion],
  );

  const doUpdate = () => {
    if (!info) return;
    const from = info.current;
    const ok = window.confirm(
      `Yakin ingin memperbarui ke ${info.latest}?\n\n` +
        "Pembaruan akan merestart program, tugas yang sedang berjalan akan terputus.\n" +
        (info.mode === "docker"
          ? "\nPerhatian: pembaruan di dalam container hanya mengganti programnya saja, tidak memperbarui tool chain seperti playwright / nmap di dalam image; " +
            "jika versi baru membutuhkan tool baru, gunakan docker compose pull."
          : ""),
    );
    if (!ok) return;

    setBusy(true);
    setProgress({ phase: "downloading", percent: 0, message: "Mempersiapkan..." });
    const es = openStream(from);
    api.applyUpdate().catch((e) => {
      es.close();
      setBusy(false);
      setProgress(null);
      toast.error("Gagal memulai pembaruan: " + (e as Error).message);
    });
  };

  const doRollback = () => {
    if (!info) return;
    if (
      !window.confirm(
        "Rollback ke versi sebelumnya?\n\nProgram akan direstart, tugas yang sedang berjalan akan terputus.\nPerhatian: struktur database tidak akan dirollback, versi lama mungkin tidak bisa mengenali data yang ditulis oleh versi baru.",
      )
    )
      return;
    const from = info.current;
    setBusy(true);
    api
      .rollbackUpdate()
      .then(() => {
        toast.success("Berhasil beralih ke versi sebelumnya, sedang restart...");
        void waitForNewVersion(from);
      })
      .catch((e) => {
        setBusy(false);
        toast.error("Rollback gagal: " + (e as Error).message);
      });
  };

  const phase = progress?.phase;
  const showProgress = busy || restarting;
  // Hanya fase download yang mendapat persentase sungguhan (dihitung dari Content-Length). Verifikasi/
  // ekstraksi/menunggu restart adalah fase yang durasinya tidak bisa diprediksi, progress bar diisi
  // penuh dan diberi animasi pulse untuk menunjukkan "sedang sibuk tapi tidak tahu pasti berapa lama lagi".
  const downloading = !restarting && phase === "downloading";
  const pct = downloading ? Math.max(progress?.percent ?? 0, 0) : 100;

  return (
    // Halaman pengaturan memakai layout masonry multi-kolom, kartu mengatur sendiri jarak antar baris dan melarang terputus antar kolom (lihat komentar di page.tsx).
    <Card className="mb-4 break-inside-avoid md:mb-6">
      <CardHeader>
        <CardTitle className="flex items-center gap-2 text-base">
          <DownloadIcon className="size-4" />
          Versi dan Pembaruan
        </CardTitle>
        <CardDescription>Periksa dan instal versi baru dari GitHub. Pembaruan akan merestart program, tugas yang sedang berjalan akan terputus.</CardDescription>
      </CardHeader>
      <CardContent className="space-y-4">
        <div className="flex flex-wrap items-center gap-2 text-sm">
          <span className="text-muted-foreground">Versi Saat Ini</span>
          <Badge variant="secondary" className="font-mono">
            {info?.current ?? "…"}
          </Badge>
          {info && (
            <>
              <Badge variant="outline" className="font-mono">
                {info.os}/{info.arch}
              </Badge>
              <Badge variant="outline">{info.mode === "docker" ? "Docker" : "Program Mandiri"}</Badge>
            </>
          )}
          {info?.latest && (
            <>
              <span className="text-muted-foreground">Versi Terbaru</span>
              <Badge variant={info.has_update ? "default" : "secondary"} className="font-mono">
                {info.latest}
              </Badge>
            </>
          )}
          {info?.html_url && (
            <a
              href={info.html_url}
              target="_blank"
              rel="noreferrer"
              className="inline-flex items-center gap-1 text-xs text-muted-foreground underline-offset-4 hover:underline"
            >
              Log Perubahan <ExternalLinkIcon className="size-3" />
            </a>
          )}
        </div>

        {info?.boot_notice && (
          <p className="flex items-start gap-2 rounded-md border border-amber-500/40 bg-amber-500/10 p-2 text-xs text-amber-700 dark:text-amber-400">
            <TriangleAlertIcon className="mt-0.5 size-3.5 shrink-0" />
            {info.boot_notice}
          </p>
        )}

        {info?.error && (
          <p className="flex items-start gap-2 rounded-md border border-destructive/40 bg-destructive/10 p-2 text-xs text-destructive">
            <TriangleAlertIcon className="mt-0.5 size-3.5 shrink-0" />
            Tidak bisa terhubung ke GitHub: {info.error}
            {" "}Bisa dicoba lagi setelah mengatur proxy global di atas.
          </p>
        )}

        {info && !info.comparable && info.reason && <p className="text-xs text-muted-foreground">{info.reason}</p>}

        {info?.has_update && info.asset_available === false && (
          <p className="flex items-start gap-2 rounded-md border border-destructive/40 bg-destructive/10 p-2 text-xs text-destructive">
            <TriangleAlertIcon className="mt-0.5 size-3.5 shrink-0" />
            {info.latest} tidak menyediakan paket rilis untuk {info.os}/{info.arch} (tidak ada {info.asset}), tidak bisa diperbarui otomatis.
          </p>
        )}

        {info?.has_update && info.asset_available !== false && (
          <p className="text-xs text-muted-foreground">
            Akan mengunduh <span className="font-mono">{info.asset}</span>
            {info.size ? ` (${humanSize(info.size)})` : ""}, baru diganti setelah verifikasi SHA256 dan smoke test lolos, gagal otomatis mempertahankan versi saat ini.
          </p>
        )}

        {info && !info.has_update && info.comparable && !info.error && (
          <p className="flex items-center gap-2 text-xs text-muted-foreground">
            <CheckCircle2Icon className="size-3.5 text-emerald-600" />
            Sudah versi terbaru.
          </p>
        )}

        {info?.mode === "docker" && info.has_update && (
          <p className="text-xs text-muted-foreground">
            Pembaruan di Docker hanya mengganti programnya saja, tidak memperbarui tool chain seperti playwright / nmap di dalam image, dan
            <span className="font-mono"> docker compose up -d </span>
            setelah membuat ulang container akan kembali ke versi bawaan image. Untuk upgrade sekaligus image-nya, jalankan
            <span className="font-mono"> docker compose pull artex &amp;&amp; docker compose up -d artex</span>.
          </p>
        )}

        {showProgress && (
          <div className="space-y-1.5">
            <Progress value={pct} className={downloading ? undefined : "animate-pulse"} />
            <p className="text-xs text-muted-foreground">
              {restarting ? "Sedang merestart dan menerapkan versi baru, mohon tunggu (halaman akan dimuat ulang otomatis)..." : progress?.message}
            </p>
          </div>
        )}

        <div className="flex flex-wrap gap-2">
          <Button variant="outline" size="sm" onClick={() => check(false)} disabled={checking || busy || restarting}>
            <RefreshCwIcon className={checking ? "size-4 animate-spin" : "size-4"} />
            Cek Pembaruan
          </Button>
          <Button
            size="sm"
            onClick={doUpdate}
            disabled={busy || restarting || !info?.has_update || info?.asset_available === false}
          >
            <DownloadIcon className="size-4" />
            {info?.has_update ? `Perbarui ke ${info.latest}` : "Perbarui Sekarang"}
          </Button>
          {info?.has_backup && (
            <Button variant="ghost" size="sm" onClick={doRollback} disabled={busy || restarting}>
              <RotateCcwIcon className="size-4" />
              Rollback ke Versi Sebelumnya
            </Button>
          )}
        </div>

        <p className="text-xs text-muted-foreground">
          Pembaruan sekali klik mengandalkan script daemon untuk merestart program. Jalankan ARTEX lewat{" "}
          <span className="font-mono">start.sh</span> (untuk Windows pakai
          <span className="font-mono"> start.bat</span>); jika binary artex dijalankan langsung, program tidak akan otomatis dihidupkan lagi setelah keluar.
        </p>
      </CardContent>
    </Card>
  );
}
