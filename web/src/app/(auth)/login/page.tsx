"use client";

import { useEffect, useRef, useState } from "react";

import { useRouter } from "next/navigation";

import { AlertTriangle, ShieldCheck } from "lucide-react";

import { Button } from "@/components/ui/button";
import { Checkbox } from "@/components/ui/checkbox";
import { Dialog, DialogClose, DialogContent, DialogFooter, DialogHeader, DialogTitle } from "@/components/ui/dialog";
import { Input } from "@/components/ui/input";
import { Label } from "@/components/ui/label";
import { api } from "@/lib/api";
import { auth } from "@/lib/auth";

export default function LoginPage() {
  const router = useRouter();
  const [password, setPassword] = useState("");
  const [error, setError] = useState("");
  const [loading, setLoading] = useState(false);
  const [checking, setChecking] = useState(true);
  const [agreed, setAgreed] = useState(false);
  const [termsOpen, setTermsOpen] = useState(false);
  const [readToEnd, setReadToEnd] = useState(false);
  const termsBodyRef = useRef<HTMLDivElement>(null);

  // Bisa klik "Setuju" hanya setelah discroll sampai ke bawah syarat (termasuk kasus yang sudah tampil penuh tanpa perlu discroll).
  function handleTermsScroll() {
    const el = termsBodyRef.current;
    if (!el) return;
    if (el.scrollTop + el.clientHeight >= el.scrollHeight - 8) setReadToEnd(true);
  }

  useEffect(() => {
    if (!termsOpen) return;
    // Reset saat dibuka, dan menangani kasus saat konten sudah muat dalam satu layar sehingga scroll tidak akan pernah terpicu.
    setReadToEnd(false);
    const el = termsBodyRef.current;
    if (el && el.scrollHeight <= el.clientHeight + 8) setReadToEnd(true);
  }, [termsOpen]);

  useEffect(() => {
    // Kalau sudah login langsung masuk ke halaman utama (pada static export tidak ada middleware yang menangani redirect ini).
    const token = auth.getToken();
    if (token) {
      // localStorage mungkin masih menyimpan kredensial tapi cookie-nya sudah hilang. Sinkronkan dulu, baru kirim request baru,
      // supaya guard di server atau cache routing tidak mengarahkan balik ke halaman login yang masih berstatus checking.
      auth.setToken(token);
      window.location.replace("/function/tasks");
      return;
    }
    api
      .authStatus()
      .then(({ initialized }) => {
        if (!initialized) router.replace("/setup");
      })
      .catch(() => setError("Tidak bisa terhubung ke layanan backend"))
      .finally(() => setChecking(false));
  }, [router]);

  async function handleSubmit(e: React.FormEvent) {
    e.preventDefault();
    if (!agreed) {
      setError("Harap baca dan setujui dulu \"Ketentuan Penggunaan\" terlebih dahulu");
      return;
    }
    setLoading(true);
    setError("");
    try {
      const { token } = await api.login("ARTEX", password);
      auth.setToken(token);
      window.location.replace("/function/tasks");
    } catch {
      setError("Username atau password salah");
    } finally {
      setLoading(false);
    }
  }

  if (checking) {
    return (
      <div role="status" className="flex min-h-dvh items-center justify-center text-muted-foreground">
        Sedang memeriksa status login...
      </div>
    );
  }

  return (
    <div className="flex h-dvh">
      {/* Left panel */}
      <div className="hidden flex-col items-center justify-center bg-primary p-12 text-center lg:flex lg:w-1/3">
        <div className="relative flex items-center justify-center">
          <div className="absolute size-80 rounded-full border border-primary-foreground/10" />
          <div className="absolute size-60 rounded-full border border-primary-foreground/15" />
          <div className="absolute size-40 rounded-full border border-primary-foreground/20" />
          {/* eslint-disable-next-line @next/next/no-img-element */}
          <img src="/logo.png" alt="ARTEX" width={160} height={160} className="relative brightness-0 invert" />
        </div>
      </div>

      {/* Right panel */}
      <div className="flex w-full items-center justify-center bg-background p-8 lg:w-2/3">
        <div className="w-full max-w-md space-y-10 py-24 lg:py-32">
          <div className="space-y-4 text-center">
            <h2 className="text-2xl font-medium tracking-tight">Masuk</h2>
            <p className="mx-auto max-w-xl text-muted-foreground">Selamat datang kembali, masukkan password untuk melanjutkan ke ARTEX</p>
          </div>
          <form onSubmit={handleSubmit} className="flex flex-col gap-4">
            <div className="space-y-1.5">
              <Label htmlFor="username">Username</Label>
              <Input id="username" value="ARTEX" readOnly className="bg-muted text-muted-foreground" />
            </div>
            <div className="space-y-1.5">
              <Label htmlFor="password">Password</Label>
              <Input
                id="password"
                type="password"
                value={password}
                onChange={(e) => setPassword(e.target.value)}
                placeholder="Masukkan password"
                autoFocus
                autoComplete="current-password"
              />
            </div>
            <div className="flex items-start gap-2">
              <Checkbox
                id="agree-terms"
                checked={agreed}
                onCheckedChange={(v) => setAgreed(v === true)}
                className="mt-0.5"
              />
              <Label htmlFor="agree-terms" className="text-sm font-normal leading-relaxed text-muted-foreground">
                Saya sudah membaca dan menyetujui
                <button
                  type="button"
                  onClick={() => setTermsOpen(true)}
                  className="mx-0.5 font-medium text-primary underline-offset-4 hover:underline"
                >
                  "Ketentuan Penggunaan"
                </button>
              </Label>
            </div>
            {error && <p className="text-sm text-destructive">{error}</p>}
            <Button type="submit" className="w-full" disabled={loading || !password || !agreed}>
              {loading ? "Sedang masuk..." : "Masuk"}
            </Button>
          </form>
        </div>
      </div>

      <Dialog open={termsOpen} onOpenChange={setTermsOpen}>
        <DialogContent className="gap-0 p-0 sm:max-w-2xl">
          <DialogHeader className="flex-row items-center gap-3 border-b px-6 py-4">
            <div className="flex size-10 shrink-0 items-center justify-center rounded-lg bg-primary/10 text-primary">
              <ShieldCheck className="size-5" />
            </div>
            <div className="space-y-0.5">
              <DialogTitle className="text-base">Ketentuan Penggunaan dan Penafian ARTEX</DialogTitle>
              <p className="text-xs text-muted-foreground">
                Versi v1.0 · Berlaku sejak 2026-09-18 · Harap baca seluruh ketentuan berikut sebelum login
              </p>
            </div>
          </DialogHeader>

          <div
            ref={termsBodyRef}
            onScroll={handleTermsScroll}
            className="max-h-[60vh] space-y-5 overflow-y-auto px-6 py-5 text-sm leading-relaxed text-muted-foreground"
          >
            <p className="rounded-lg border bg-muted/40 p-3 text-foreground/80">
              "Ketentuan Penggunaan dan Penafian" ini (selanjutnya disebut "Pernyataan ini") merupakan kesepakatan antara Anda dan penulis
              serta kontributor proyek ARTEX terkait penggunaan perangkat lunak ini. Mohon baca dengan saksama dan pahami sepenuhnya seluruh ketentuan sebelum menggunakan, terutama pasal penafian, pembatasan tanggung jawab, dan larangan yang ditandai dengan huruf tebal atau blok warna.
              <span className="font-medium text-foreground">
                {" "}
                Begitu Anda mengunduh, memasang, mengakses, atau menggunakan perangkat lunak ini dengan cara apa pun, Anda dianggap telah membaca, memahami, dan menyetujui untuk terikat sepenuhnya oleh Pernyataan ini.
              </span>
            </p>

            <section className="space-y-1.5">
              <h4 className="flex items-center gap-2 font-medium text-foreground">
                <span className="flex size-5 items-center justify-center rounded-md bg-muted text-xs font-semibold text-muted-foreground">
                  1
                </span>
                Pasal 1 · Definisi dan Lisensi Open Source
              </h4>
              <p className="pl-7">
                Perangkat lunak ini (ARTEX) adalah program open source yang dirilis berdasarkan GNU Affero General Public License
                v3.0 (AGPL-3.0). Anda bebas menggunakan, menyalin, memodifikasi, dan mendistribusikan perangkat lunak ini sesuai lisensi tersebut; namun setiap karya turunan (termasuk layanan online yang disediakan ke pihak ketiga melalui jaringan) wajib tetap open source dengan
                lisensi AGPL-3.0 yang sama dan membuka kode sumber lengkapnya kepada penggunanya. Ketentuan lengkap AGPL-3.0 mengacu pada file LICENSE yang disertakan.
              </p>
            </section>

            <section className="space-y-1.5">
              <h4 className="flex items-center gap-2 font-medium text-foreground">
                <span className="flex size-5 items-center justify-center rounded-md bg-muted text-xs font-semibold text-muted-foreground">
                  2
                </span>
                Pasal 2 · Ruang Lingkup Penggunaan yang Diizinkan
              </h4>
              <p className="pl-7">
                Perangkat lunak ini hanya untuk pembelajaran pribadi, riset kode, diskusi prinsip teknis keamanan, dan verifikasi teknis di lingkungan lokal terisolasi yang Anda bangun sendiri; cocok untuk penggunaan non-ofensif dan non-destruktif seperti belajar, riset akademis, dan peninjauan kode. Di luar yang diizinkan secara eksplisit dalam pasal ini, Anda dilarang menggunakan perangkat lunak ini untuk tujuan lain apa pun.
              </p>
            </section>

            <section className="space-y-2">
              <h4 className="flex items-center gap-2 font-medium text-destructive">
                <span className="flex size-5 items-center justify-center rounded-md bg-destructive/10 text-xs font-semibold text-destructive">
                  3
                </span>
                <AlertTriangle className="size-4" />
                Pasal 3 · Perbuatan yang Dilarang
              </h4>
              <ul className="ml-7 list-decimal space-y-1.5 rounded-lg border border-destructive/20 bg-destructive/5 p-3 pl-8 text-foreground/80 marker:text-destructive/70">
                <li>
                  Dilarang keras melakukan pemindaian, pengujian, eksploitasi, atau serangan terhadap situs web, layanan online, atau sistem yang terhubung jaringan milik orang lain atau pihak ketiga mana pun (terlepas apakah sudah mendapat izin atau merupakan aset milik Anda sendiri);
                </li>
                <li>Dilarang keras menggunakan perangkat lunak ini untuk pengujian penetrasi nyata, latihan serang-bertahan, simulasi red-blue team, atau lingkungan produksi;</li>
                <li>Dilarang keras menggunakan perangkat lunak ini untuk peretasan ilegal, pencurian data, pemerasan, serangan penolakan layanan (DoS/DDoS), atau aktivitas destruktif maupun kriminal lainnya;</li>
                <li>Dilarang keras menghapus, mengubah, atau menghindari notifikasi hak cipta, lisensi, atau peringatan keamanan apa pun pada perangkat lunak ini maupun keluarannya;</li>
                <li>Dilarang keras melakukan perbuatan apa pun yang melanggar hukum, peraturan, dan ketentuan regulasi di negara atau wilayah tempat Anda berada.</li>
              </ul>
            </section>

            <section className="space-y-1.5">
              <h4 className="flex items-center gap-2 font-medium text-foreground">
                <span className="flex size-5 items-center justify-center rounded-md bg-muted text-xs font-semibold text-muted-foreground">
                  4
                </span>
                Pasal 4 · Kekayaan Intelektual
              </h4>
              <p className="pl-7">
                Hak cipta dan kekayaan intelektual terkait perangkat lunak ini dimiliki oleh penulis proyek dan kontributornya, dan hak terkait diberikan kepada Anda sesuai cakupan yang diatur dalam lisensi
                AGPL-3.0. Di luar hak yang secara eksplisit diberikan oleh lisensi tersebut, Pernyataan ini tidak memberikan hak lain apa pun kepada Anda, baik secara tersurat maupun tersirat.
              </p>
            </section>

            <section className="space-y-1.5">
              <h4 className="flex items-center gap-2 font-medium text-foreground">
                <span className="flex size-5 items-center justify-center rounded-md bg-muted text-xs font-semibold text-muted-foreground">
                  5
                </span>
                Pasal 5 · Data dan Privasi
              </h4>
              <p className="pl-7">
                Perangkat lunak ini adalah program open source yang dapat Anda deploy sendiri; penulis tidak mengoperasikan layanan terpusat apa pun dan tidak mengumpulkan atau mengunggah data penggunaan Anda. Seluruh data yang dihasilkan, diproses, atau Anda akses selama penggunaan sepenuhnya berada di bawah kendali dan tanggung jawab Anda, termasuk legalitas dan keamanannya; segala akibat dari penanganan data yang tidak tepat menjadi tanggung jawab Anda sendiri.
              </p>
            </section>

            <section className="space-y-1.5">
              <h4 className="flex items-center gap-2 font-medium text-foreground">
                <span className="flex size-5 items-center justify-center rounded-md bg-muted text-xs font-semibold text-muted-foreground">
                  6
                </span>
                Pasal 6 · Kepatuhan dan Tanggung Jawab Hukum
              </h4>
              <p className="pl-7">
                Anda wajib mematuhi sendiri seluruh hukum dan peraturan di negara atau wilayah Anda terkait keamanan siber, keamanan data, perlindungan informasi pribadi, kejahatan komputer, dan hal terkait lainnya (di Tiongkok daratan termasuk namun tidak terbatas pada Undang-Undang Keamanan Siber, Undang-Undang Keamanan Data, Undang-Undang Perlindungan Informasi Pribadi, serta interpretasi yudisial terkait).
                <span className="font-medium text-foreground">
                  {" "}
                  Segala tanggung jawab hukum dan akibat yang timbul dari pelanggaran Anda terhadap hukum dan peraturan di atas atau ketentuan dalam Pernyataan ini sepenuhnya ditanggung oleh Anda sendiri, dan tidak terkait dengan penulis maupun kontributor perangkat lunak ini.
                </span>
              </p>
            </section>

            <section className="space-y-1.5">
              <h4 className="flex items-center gap-2 font-medium text-foreground">
                <span className="flex size-5 items-center justify-center rounded-md bg-muted text-xs font-semibold text-muted-foreground">
                  7
                </span>
                Pasal 7 · Penafian dan Pembatasan Tanggung Jawab
              </h4>
              <p className="pl-7">
                Perangkat lunak ini disediakan dalam kondisi "APA ADANYA (AS IS)" dan "SEBAGAIMANA TERSEDIA (AS
                AVAILABLE)", tanpa jaminan apa pun baik tersurat maupun tersirat, termasuk namun tidak terbatas pada jaminan kelayakan jual, kesesuaian untuk tujuan tertentu, akurasi, dan non-pelanggaran hak. Sejauh diizinkan oleh hukum yang berlaku, penulis dan kontributor perangkat lunak ini tidak bertanggung jawab atas kerugian langsung, tidak langsung, insidental, khusus, atau konsekuensial apa pun yang timbul dari penggunaan atau ketidakmampuan menggunakan perangkat lunak ini (terlepas dari tepat atau tidaknya cara penggunaan), termasuk namun tidak terbatas pada kehilangan data, kerusakan sistem, gangguan bisnis, kehilangan keuntungan, atau sengketa hukum.
              </p>
            </section>

            <section className="space-y-1.5">
              <h4 className="flex items-center gap-2 font-medium text-foreground">
                <span className="flex size-5 items-center justify-center rounded-md bg-muted text-xs font-semibold text-muted-foreground">
                  8
                </span>
                Pasal 8 · Perubahan Ketentuan dan Interpretasi Akhir
              </h4>
              <p className="pl-7">
                Penulis berhak memperbarui Pernyataan ini dari waktu ke waktu sesuai kebutuhan hukum atau perkembangan proyek; versi yang diperbarui akan dirilis bersama proyek dan berlaku sejak tanggal pengumumannya. Dengan terus menggunakan perangkat lunak ini, Anda dianggap menerima ketentuan yang telah direvisi. Sejauh diizinkan oleh hukum, hak interpretasi akhir atas Pernyataan ini berada pada penulis proyek. Jika salah satu pasal dalam Pernyataan ini dinyatakan tidak berlaku, hal itu tidak mempengaruhi keberlakuan pasal-pasal lainnya.
              </p>
            </section>
          </div>

          <DialogFooter className="mx-0 mb-0 flex-col items-stretch gap-2 rounded-b-xl px-6 sm:flex-row sm:items-center sm:justify-between">
            <p className="text-xs text-muted-foreground">
              {readToEnd ? "Anda sudah membaca seluruh ketentuan" : "Scroll ketentuan sampai ke bawah sebelum konfirmasi"}
            </p>
            <DialogClose asChild>
              <Button
                type="button"
                disabled={!readToEnd}
                onClick={() => {
                  setAgreed(true);
                  setError("");
                }}
              >
                Saya sudah membaca dan menyetujui semua ketentuan
              </Button>
            </DialogClose>
          </DialogFooter>
        </DialogContent>
      </Dialog>
    </div>
  );
}
