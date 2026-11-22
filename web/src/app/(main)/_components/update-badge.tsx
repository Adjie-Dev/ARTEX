"use client";

import * as React from "react";

import Link from "next/link";

import { ArrowUpCircleIcon } from "lucide-react";

import { api } from "@/lib/api";

/**
 * Indikator "ada versi baru" di topbar: dicek sekali saat halaman dimuat, kalau ada update akan muncul
 * di samping nomor versi. Klik langsung menuju kartu "Versi dan Update" di halaman pengaturan sistem.
 *
 * Hasil query ke GitHub di-cache backend selama 30 menit, jadi query sekali tiap mount di sini aman
 * dilakukan. GitHub API yang tidak terautentikasi hanya punya kuota 60 kali/jam/IP, tanpa cache itu,
 * membuka beberapa tab sekaligus akan menghabiskan kuota, dan saat benar-benar ingin mengecek update
 * malah tidak bisa.
 *
 * Kegagalan query selalu didiamkan: topbar bukan tempat untuk menampilkan error, pengguna yang masuk ke
 * halaman pengaturan dan klik "Cek Update" akan melihat alasannya.
 */
export function UpdateBadge() {
  const [latest, setLatest] = React.useState("");

  React.useEffect(() => {
    let alive = true;
    api
      .checkUpdate()
      .then((r) => {
        // has_update sudah mencakup pengecekan "nomor versi dapat dibandingkan", build development tidak akan menampilkan indikator ini.
        if (alive && r.has_update && r.latest) setLatest(r.latest.replace(/^v(?=\d)/, ""));
      })
      .catch(() => {
        // Didiamkan: tidak ada koneksi / GitHub rate limit, keduanya tidak seharusnya memunculkan error di topbar.
      });
    return () => {
      alive = false;
    };
  }, []);

  if (!latest) return null;

  return (
    <Link
      href="/system/settings"
      title={`Versi baru ${latest} ditemukan, klik untuk memperbarui`}
      className="inline-flex items-center gap-1.5 rounded-full bg-primary px-2.5 py-1 font-medium text-primary-foreground text-xs transition-opacity hover:opacity-90"
    >
      {/* Titik berkedip: topbar punya banyak elemen, teks polos mudah terlewat, animasi membuatnya langsung terlihat. */}
      <span className="relative flex size-1.5">
        <span className="absolute inline-flex size-full animate-ping rounded-full bg-primary-foreground opacity-75" />
        <span className="relative inline-flex size-1.5 rounded-full bg-primary-foreground" />
      </span>
      <ArrowUpCircleIcon className="size-3.5" />
      <span className="hidden sm:inline">Versi baru {latest}</span>
      <span className="sm:hidden">Versi baru</span>
    </Link>
  );
}
