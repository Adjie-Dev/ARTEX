"use client";

import * as React from "react";

import { CpuIcon, FlaskConicalIcon, KeyboardIcon, RadioTowerIcon, SearchIcon, ShieldAlertIcon } from "lucide-react";
import { toast } from "sonner";

import { Button } from "@/components/ui/button";
import { Card, CardContent, CardDescription, CardHeader, CardTitle } from "@/components/ui/card";
import { Input } from "@/components/ui/input";
import { Label } from "@/components/ui/label";
import { Select, SelectContent, SelectItem, SelectTrigger, SelectValue } from "@/components/ui/select";
import { Switch } from "@/components/ui/switch";
import { api } from "@/lib/api";
import { CHAT_SEND_MODE_OPTIONS, type ChatSendMode, setChatSendMode, useChatSendMode } from "@/lib/chat-send-mode";
import type { Settings } from "@/lib/types";

import { UpdateCard } from "./_components/update-card";

export default function SystemSettingsPage() {
  const [trafficCapture, setTrafficCapture] = React.useState(false);
  const [agentTrafficBinding, setAgentTrafficBinding] = React.useState(false);
  const [webSearch, setWebSearch] = React.useState(false);
  const [backend, setBackend] = React.useState("ddgs");
  const [braveKeySet, setBraveKeySet] = React.useState(false);
  const [braveKeyInput, setBraveKeyInput] = React.useState("");
  const [tavilyKeySet, setTavilyKeySet] = React.useState(false);
  const [tavilyKeyInput, setTavilyKeyInput] = React.useState("");
  const [savingTavilyKey, setSavingTavilyKey] = React.useState(false);
  const [proxyInput, setProxyInput] = React.useState("");
  const [savingProxy, setSavingProxy] = React.useState(false);
  const [globalProxyInput, setGlobalProxyInput] = React.useState("");
  const [savingGlobalProxy, setSavingGlobalProxy] = React.useState(false);
  const [testing, setTesting] = React.useState(false);
  const [loaded, setLoaded] = React.useState(false);
  const [saving, setSaving] = React.useState(false);
  const [savingKey, setSavingKey] = React.useState(false);
  const [pyInterp, setPyInterp] = React.useState("");
  const [workers, setWorkers] = React.useState("3");
  const [savingWorkers, setSavingWorkers] = React.useState(false);
  // Rentang injeksi batasan operasi (default semua aktif).
  const [injectPlanner, setInjectPlanner] = React.useState(true);
  const [injectWorker, setInjectWorker] = React.useState(true);
  // Fitur eksperimental: kompresi konteks noa (default nonaktif).
  const [noaCompaction, setNoaCompaction] = React.useState(false);
  // Preferensi murni frontend: tidak lewat /api/settings, langsung baca/tulis localStorage.
  const sendMode = useChatSendMode();

  const apply = React.useCallback((s: Settings) => {
    setTrafficCapture(!!s.traffic_capture);
    setAgentTrafficBinding(!!s.agent_traffic_binding);
    setWebSearch(!!s.web_search_enabled);
    setBackend(s.web_search_backend || "ddgs");
    setBraveKeySet(!!s.brave_key_set);
    setTavilyKeySet(!!s.tavily_key_set);
    setProxyInput(s.web_search_proxy ?? "");
    setGlobalProxyInput(s.global_proxy ?? "");
    setPyInterp(s.python_interpreter ?? "");
    setWorkers(String(s.workers ?? 3));
    setInjectPlanner(s.constraints_inject_planner !== false);
    setInjectWorker(s.constraints_inject_worker !== false);
    setNoaCompaction(!!s.noa_compaction);
  }, []);

  const saveWorkers = () => {
    const n = Number(workers);
    if (!Number.isInteger(n) || n <= 0) {
      toast.error("Jumlah konkurensi harus berupa bilangan bulat lebih besar dari 0");
      return;
    }
    setSavingWorkers(true);
    api
      .setSettings({ workers: n })
      .then((s) => {
        apply(s);
        toast.success("Jumlah work agent paralel berhasil disimpan (berlaku untuk tugas yang dimulai setelah ini)");
      })
      .catch((e) => toast.error("Gagal menyimpan: " + (e as Error).message))
      .finally(() => setSavingWorkers(false));
  };

  const savePython = () => {
    setSaving(true);
    api
      .setSettings({ python_interpreter: pyInterp.trim() })
      .then((s) => {
        apply(s);
        toast.success("Konfigurasi interpreter Python berhasil disimpan");
      })
      .catch((e) => toast.error("Gagal menyimpan: " + (e as Error).message))
      .finally(() => setSaving(false));
  };
  const detectPython = () => {
    setSaving(true);
    api
      .detectPython()
      .then((r) => setPyInterp(r.python_interpreter))
      .catch(() => undefined)
      .finally(() => setSaving(false));
  };

  React.useEffect(() => {
    api
      .settings()
      .then(apply)
      .catch(() => undefined)
      .finally(() => setLoaded(true));
  }, [apply]);

  const toggleTraffic = (v: boolean) => {
    setTrafficCapture(v); // optimistic
    setSaving(true);
    api
      .setSettings({ traffic_capture: v })
      .then(apply)
      .catch(() => setTrafficCapture(!v)) // revert on failure
      .finally(() => setSaving(false));
  };

  const toggleInjectPlanner = (v: boolean) => {
    setInjectPlanner(v); // optimistic
    api
      .setSettings({ constraints_inject_planner: v })
      .then(apply)
      .catch(() => setInjectPlanner(!v)); // revert on failure
  };

  const toggleAgentTrafficBinding = (v: boolean) => {
    setAgentTrafficBinding(v);
    setSaving(true);
    api
      .setSettings({ agent_traffic_binding: v })
      .then((s) => {
        apply(s);
        toast.success(v ? "Auto-binding trafik Agent diaktifkan" : "Auto-binding trafik Agent dinonaktifkan");
      })
      .catch((e) => {
        setAgentTrafficBinding(!v);
        toast.error(`Gagal menyimpan: ${(e as Error).message}`);
      })
      .finally(() => setSaving(false));
  };

  const toggleInjectWorker = (v: boolean) => {
    setInjectWorker(v); // optimistic
    api
      .setSettings({ constraints_inject_worker: v })
      .then(apply)
      .catch(() => setInjectWorker(!v)); // revert on failure
  };

  const toggleNoaCompaction = (v: boolean) => {
    setNoaCompaction(v); // optimistic
    api
      .setSettings({ noa_compaction: v })
      .then((s) => {
        apply(s);
        toast.success(
          v ? "Kompresi konteks noa diaktifkan (berlaku untuk proses yang dimulai setelah ini)" : "Kompresi konteks noa dinonaktifkan (kembali ke kompresi bawaan)",
        );
      })
      .catch((e) => {
        setNoaCompaction(!v); // revert on failure
        toast.error(`Gagal menyimpan: ${(e as Error).message}`);
      });
  };

  // Persist a web-search patch (enable and/or backend). Optimistic with refetch.
  const saveWebSearch = (patch: Partial<Settings>) => {
    setSaving(true);
    api
      .setSettings(patch)
      .then((s) => {
        apply(s);
        toast.success("Konfigurasi pencarian web berhasil disimpan");
      })
      .catch((e) => {
        toast.error("Gagal menyimpan: " + (e as Error).message);
        api
          .settings()
          .then(apply)
          .catch(() => undefined);
      })
      .finally(() => setSaving(false));
  };

  const saveBraveKey = () => {
    setSavingKey(true);
    api
      .setSettings({ brave_search_api_key: braveKeyInput })
      .then((s) => {
        apply(s);
        setBraveKeyInput("");
        toast.success("Brave API Key berhasil disimpan");
      })
      .catch((e) => toast.error("Gagal menyimpan: " + (e as Error).message))
      .finally(() => setSavingKey(false));
  };

  const saveTavilyKey = () => {
    setSavingTavilyKey(true);
    api
      .setSettings({ tavily_search_api_key: tavilyKeyInput })
      .then((s) => {
        apply(s);
        setTavilyKeyInput("");
        toast.success("Tavily API Key berhasil disimpan");
      })
      .catch((e) => toast.error("Gagal menyimpan: " + (e as Error).message))
      .finally(() => setSavingTavilyKey(false));
  };

  const saveProxy = () => {
    setSavingProxy(true);
    api
      .setSettings({ web_search_proxy: proxyInput.trim() })
      .then((s) => {
        apply(s);
        toast.success(proxyInput.trim() ? "Proxy keluar berhasil disimpan" : "Proxy keluar dihapus (kembali ke koneksi langsung)");
      })
      .catch((e) => toast.error("Gagal menyimpan: " + (e as Error).message))
      .finally(() => setSavingProxy(false));
  };

  const saveGlobalProxy = () => {
    setSavingGlobalProxy(true);
    api
      .setSettings({ global_proxy: globalProxyInput.trim() })
      .then((s) => {
        apply(s);
        toast.success(globalProxyInput.trim() ? "Proxy global berhasil disimpan" : "Proxy global dihapus (kembali ke koneksi langsung)");
      })
      .catch((e) => toast.error("Gagal menyimpan: " + (e as Error).message))
      .finally(() => setSavingGlobalProxy(false));
  };

  // Run a real "test" search ("test") against the CURRENT form values (backend +
  // proxy + entered key), falling back to saved values server-side. Toasts result.
  const runTest = () => {
    setTesting(true);
    api
      .testWebSearch({
        web_search_backend: backend,
        web_search_proxy: proxyInput.trim(),
        brave_search_api_key: braveKeyInput,
        tavily_search_api_key: tavilyKeyInput,
      })
      .then((r) => {
        if (r.ok) toast.success(`Tes pencarian berhasil · ${r.backend} mengembalikan ${r.count} hasil`);
        else toast.error("Tes pencarian gagal: " + (r.error || "Error tidak diketahui"));
      })
      .catch((e) => toast.error("Tes pencarian gagal: " + (e as Error).message))
      .finally(() => setTesting(false));
  };

  // brave-free selected but no key stored and none being entered → tool stays off.
  const braveNeedsKey = webSearch && backend === "brave-free" && !braveKeySet;

  return (
    <div className="flex flex-1 flex-col gap-4 md:gap-6">
      <div>
        <h1 className="text-xl font-semibold tracking-tight">Konfigurasi Sistem</h1>
        <p className="text-muted-foreground text-sm">Saklar runtime global</p>
      </div>

      {/* Memakai multi-kolom, bukan grid: kartu pencarian web jauh lebih tinggi dari yang lain,
          dan tingginya berubah tergantung backend yang dipilih (input key brave/tavily
          dirender secara kondisional). Grid akan menyamakan tinggi semua kolom mengikuti kartu
          tertinggi dan menyisakan banyak ruang kosong di sampingnya, sedangkan multi-kolom
          otomatis mengisi berdasarkan tinggi kontennya masing-masing. Jarak antar kartu memakai
          mb, bukan gap, karena pada layout multi-kolom, column-gap hanya mengatur jarak antar
          kolom, jarak antar baris harus diberikan sendiri oleh elemen anaknya. */}
      <div className="columns-1 gap-4 md:gap-6 lg:columns-2">
        <UpdateCard />

        <Card className="mb-4 break-inside-avoid md:mb-6">
          <CardHeader>
            <CardTitle className="flex items-center gap-2 text-base">
              <RadioTowerIcon className="size-4" />
              Penangkapan Trafik
            </CardTitle>
            <CardDescription>
              Jika diaktifkan, semua trafik HTTP Agent dicatat penuh ke database melalui proxy pencatat, dan tool
              traffic_search / traffic_get beserta konfigurasi proxy akan disuntikkan ke Agent (prompt menyertakan
              penjelasan proxy).
              <br />
              Saat dinonaktifkan (default), tidak ada trafik yang dicatat: Agent
              <b> tidak akan</b> mendapatkan konfigurasi proxy maupun tool trafik, dan prompt juga{" "}
              <b>tidak menyertakan</b> konten terkait proxy. Perubahan langsung berlaku dengan membangun ulang Agent.
            </CardDescription>
          </CardHeader>
          <CardContent className="flex items-center justify-between gap-4">
            <Label htmlFor="traffic-capture" className="text-sm font-normal text-muted-foreground">
              {trafficCapture ? "Aktif · sedang mencatat trafik dan menyuntikkan proxy" : "Nonaktif · tidak mencatat, tidak menyuntikkan proxy"}
            </Label>
            <Switch
              id="traffic-capture"
              checked={trafficCapture}
              disabled={!loaded || saving}
              onCheckedChange={toggleTraffic}
            />
          </CardContent>
        </Card>

        <Card className="mb-4 break-inside-avoid md:mb-6">
          <CardHeader>
            <CardTitle className="flex items-center gap-2 text-base">
              <RadioTowerIcon className="size-4" />
              Auto-Binding Trafik Agent
            </CardTitle>
            <CardDescription id="agent-traffic-binding-description">
              Default nonaktif. Jika diaktifkan, Agent pelapor yang terpicu saat kerentanan dimasukkan ke database
              akan memeriksa request/response HTTP yang sudah ada, mengaitkan trafik terkait sebelum menyusun
              laporan. <b>Memeriksa paket data dan pemanggilan tool tambahan akan menambah konsumsi token.</b>
              <br />
              TCP, trafik yang belum ditangkap, atau tidak ada trafik yang cocok tetap bisa dilaporkan secara normal.
              Saklar ini tidak memengaruhi penangkapan trafik, binding manual, maupun peninjauan bukti yang sudah
              tersimpan. Berlaku untuk Agent pada putaran berikutnya; setelah dinonaktifkan, binding otomatis baru
              akan langsung ditolak.
            </CardDescription>
          </CardHeader>
          <CardContent className="flex items-center justify-between gap-4">
            <Label htmlFor="agent-traffic-binding" className="text-sm font-normal text-muted-foreground">
              {agentTrafficBinding ? "Aktif · akan menambah konsumsi token" : "Nonaktif · binding manual tetap bisa dilanjutkan"}
            </Label>
            <Switch
              id="agent-traffic-binding"
              aria-describedby="agent-traffic-binding-description"
              checked={agentTrafficBinding}
              disabled={!loaded || saving}
              onCheckedChange={toggleAgentTrafficBinding}
            />
          </CardContent>
        </Card>

        <Card className="mb-4 break-inside-avoid md:mb-6">
          <CardHeader>
            <CardTitle className="flex items-center gap-2 text-base">
              <RadioTowerIcon className="size-4" />
              Proxy Global
            </CardTitle>
            <CardDescription>
              <b>Trafik tujuan</b> semua Agent keluar melalui proxy ini (menyembunyikan IP sumber / lewat jump host).
              Mendukung <b>http / https / socks5</b>, bisa disertai autentikasi {" "}
              <code>user:pass</code>. Kosongkan = koneksi langsung.
              <br />
              Saat <b>penangkapan trafik</b> aktif, proxy ini menjadi <b>upstream</b> dari proxy pencatat (trafik
              tetap dicatat penuh ke database, lalu keluar lewat proxy ini); saat penangkapan dinonaktifkan, proxy
              disuntikkan langsung ke jalur keluar bash / WebFetch Agent. Terpisah dari proxy pencarian web dan proxy
              LLM.
              <br />
              <b>Catatan</b>: socks5 saat <b>penangkapan dinonaktifkan</b> bergantung pada dukungan masing-masing
              command line tool terhadap <code>ALL_PROXY</code> (curl mendukung, sebagian tool mungkin
              mengabaikannya). Jika terutama memakai socks5, sebaiknya aktifkan penangkapan trafik, karena jalur ini
              langsung ditangani sendiri oleh MITM sehingga tool tidak perlu tahu dan tetap berjalan stabil.
            </CardDescription>
          </CardHeader>
          <CardContent className="flex flex-col gap-2">
            <Label htmlFor="global-proxy" className="text-sm font-normal text-muted-foreground">
              Alamat Proxy
            </Label>
            <div className="flex items-center gap-2">
              <Input
                id="global-proxy"
                autoComplete="off"
                placeholder="socks5://user:pass@host:1080 atau http://host:port (kosongkan = koneksi langsung)"
                value={globalProxyInput}
                disabled={!loaded || savingGlobalProxy}
                onChange={(e) => setGlobalProxyInput(e.target.value)}
              />
              <Button type="button" onClick={saveGlobalProxy} disabled={!loaded || savingGlobalProxy}>
                Simpan
              </Button>
            </div>
            <p className="text-muted-foreground text-xs">
              {globalProxyInput.trim() ? "Terkonfigurasi · semua trafik tujuan keluar lewat proxy ini" : "Belum terkonfigurasi · trafik tujuan keluar langsung"}
            </p>
          </CardContent>
        </Card>

        <Card className="mb-4 break-inside-avoid md:mb-6">
          <CardHeader>
            <CardTitle className="flex items-center gap-2 text-base">
              <ShieldAlertIcon className="size-4" />
              Injeksi Batasan Operasi
            </CardTitle>
            <CardDescription>
              Jika diaktifkan, <b>batasan operasi</b> setiap tugas (entri allow/deny yang dikelola di "Batasan
              Operasi" pada ringkasan tugas) akan disisipkan ke system prompt Agent terkait, untuk membatasi ruang
              eksplorasi (misalnya "hanya uji port saat ini" atau "larang brute force").
              <br />
              Bisa dikontrol terpisah untuk injeksi ke <b>planner</b> dan <b>worker</b>; keduanya aktif secara
              default. Perubahan langsung berlaku (dibaca pada putaran berikutnya) tanpa perlu membangun ulang Agent.
              Setelah dinonaktifkan, Agent tersebut tidak lagi melihat batasan.
            </CardDescription>
          </CardHeader>
          <CardContent className="flex flex-col gap-4">
            <div className="flex items-center justify-between gap-4">
              <Label htmlFor="inject-planner" className="text-sm font-normal text-muted-foreground">
                Injeksi ke planner{injectPlanner ? " · Aktif" : " · Nonaktif"}
              </Label>
              <Switch
                id="inject-planner"
                checked={injectPlanner}
                disabled={!loaded}
                onCheckedChange={toggleInjectPlanner}
              />
            </div>
            <div className="flex items-center justify-between gap-4">
              <Label htmlFor="inject-worker" className="text-sm font-normal text-muted-foreground">
                Injeksi ke worker{injectWorker ? " · Aktif" : " · Nonaktif"}
              </Label>
              <Switch
                id="inject-worker"
                checked={injectWorker}
                disabled={!loaded}
                onCheckedChange={toggleInjectWorker}
              />
            </div>
          </CardContent>
        </Card>

        <Card className="mb-4 break-inside-avoid md:mb-6">
          <CardHeader>
            <CardTitle className="flex items-center gap-2 text-base">
              <FlaskConicalIcon className="size-4" />
              Fitur Eksperimental
            </CardTitle>
            <CardDescription>
              Mekanisme yang masih dalam tahap pengujian, default nonaktif. Bisa mengubah perilaku Agent atau
              memengaruhi stabilitas, aktifkan setelah memahami dampaknya.
              <br />
              <b>Kompresi konteks noa</b>: model secara aktif mengompresi riwayat percakapan yang panjang (norma
              v0.4.0). Jika diaktifkan, keempat jenis Agent yang terhubung ke platform (
              <b>planner / worker / Agent utama / percakapan</b>) beralih memakai noa untuk mengambil alih konteks,
              menggantikan kompresi bawaan, teks asli sebelum dikompresi akan diarsipkan ke direktori kerja tugas
              agar mudah ditelusuri kembali. Perubahan langsung berlaku (berlaku untuk proses yang dimulai setelah
              ini) tanpa perlu membangun ulang Agent; setelah dinonaktifkan, kompresi bawaan langsung dipakai
              kembali.
            </CardDescription>
          </CardHeader>
          <CardContent className="flex items-center justify-between gap-4">
            <Label htmlFor="noa-compaction" className="text-sm font-normal text-muted-foreground">
              Kompresi konteks noa{noaCompaction ? " · Aktif" : " · Nonaktif"}
            </Label>
            <Switch
              id="noa-compaction"
              checked={noaCompaction}
              disabled={!loaded}
              onCheckedChange={toggleNoaCompaction}
            />
          </CardContent>
        </Card>

        <Card className="mb-4 break-inside-avoid md:mb-6">
          <CardHeader>
            <CardTitle className="flex items-center gap-2 text-base">
              <SearchIcon className="size-4" />
              Pencarian Web
            </CardTitle>
            <CardDescription>
              Ini adalah <b>saklar utama + konfigurasi sumber</b> untuk pencarian web. Setelah diaktifkan, baru bisa
              memilih secara terpisah di <b>konfigurasi masing-masing Agent</b> apakah akan mengaktifkan{" "}
              <b>web_search</b> (hanya mengembalikan judul/tautan/ringkasan, tidak mengambil isi halaman; pengambilan
              isi dilakukan oleh WebFetch). Pencarian web <b>tidak melewati</b> proxy pencatat, terpisah dari
              penangkapan trafik.
              <br />
              Sumber yang tersedia: <b>DuckDuckGo (ddgs)</b> (tanpa Key), <b>Brave (versi gratis)</b> (perlu mengisi
              Brave API Key), {" "}
              <b>Tavily</b> (perlu mengisi Tavily API Key), atau <b>DeepSeek</b> (memakai ulang konfigurasi LLM yang
              aktif saat ini). Saat saklar utama dinonaktifkan, saklar pencarian web di setiap Agent tidak bisa
              dipakai.
            </CardDescription>
          </CardHeader>
          <CardContent className="flex flex-col gap-4">
            <div className="flex items-center justify-between gap-4">
              <Label htmlFor="web-search" className="text-sm font-normal text-muted-foreground">
                {webSearch ? "Saklar utama aktif · bisa diaktifkan terpisah di konfigurasi masing-masing Agent" : "Nonaktif · setiap Agent tidak bisa mengaktifkan pencarian web"}
              </Label>
              <Switch
                id="web-search"
                checked={webSearch}
                disabled={!loaded || saving}
                onCheckedChange={(v) => {
                  setWebSearch(v); // optimistic
                  saveWebSearch({ web_search_enabled: v });
                }}
              />
            </div>

            {webSearch && (
              <div className="flex items-center justify-between gap-4">
                <Label className="text-sm font-normal text-muted-foreground">Sumber Pencarian</Label>
                <Select
                  value={backend}
                  disabled={!loaded || saving}
                  onValueChange={(v) => {
                    setBackend(v); // optimistic
                    saveWebSearch({ web_search_backend: v });
                  }}
                >
                  <SelectTrigger className="w-48 shrink-0">
                    <SelectValue placeholder="Pilih sumber" />
                  </SelectTrigger>
                  <SelectContent>
                    <SelectItem value="ddgs">DuckDuckGo (ddgs · gratis tanpa Key)</SelectItem>
                    <SelectItem value="brave-free">Brave (versi gratis · perlu Key)</SelectItem>
                    <SelectItem value="tavily">Tavily (perlu Key)</SelectItem>
                    <SelectItem value="deepseek">DeepSeek (resmi)</SelectItem>
                  </SelectContent>
                </Select>
              </div>
            )}

            {webSearch && backend === "deepseek" && (
              <div className="border-border/60 bg-muted/30 flex flex-col gap-2 rounded-md border p-3">
                <p className="text-sm font-medium">Pencarian Web Resmi DeepSeek</p>
                <p className="text-muted-foreground text-xs leading-relaxed">
                  Sumber ini langsung memakai ulang <b>konfigurasi LLM yang sedang aktif</b>. Karena itu, sumber ini
                  <b> hanya mendukung model resmi DeepSeek</b>, dan konfigurasi tersebut{" "}
                  <b>harus memakai protokol anthropic</b>, karena endpoint protokol OpenAI milik DeepSeek tidak
                  mendukung pencarian di sisi server. Sumber ini bisa gagal berfungsi setelah konfigurasi LLM diganti.
                </p>
                <p className="text-muted-foreground text-xs leading-relaxed">
                  Berbeda dari sumber lain, pencarian dijalankan oleh <b>sisi server DeepSeek</b>: setiap pencarian
                  akan menambah satu pemanggilan model (menimbulkan biaya token), request pencarian{" "}
                  <b>tidak melewati</b> proxy keluar di atas, dan juga <b>tidak tercatat</b> dalam jejak trafik; hasil
                  yang dikembalikan <b>hanya berupa judul dan tautan</b> (tanpa ringkasan), isi halaman diambil oleh
                  WebFetch jika diperlukan.
                </p>
                <p className="text-muted-foreground text-xs leading-relaxed">
                  Apakah syarat di atas terpenuhi perlu dipastikan sendiri, sistem tidak melakukan pemeriksaan
                  otomatis; gunakan tombol "Tes Pencarian" di bawah untuk mencobanya secara langsung.
                </p>
              </div>
            )}

            {webSearch && backend === "brave-free" && (
              <div className="flex flex-col gap-2">
                <Label htmlFor="brave-key" className="text-sm font-normal text-muted-foreground">
                  Brave Search API Key
                  {braveKeySet && <span className="ml-2 text-xs text-emerald-500">Terkonfigurasi</span>}
                </Label>
                <div className="flex items-center gap-2">
                  <Input
                    id="brave-key"
                    type="password"
                    autoComplete="off"
                    placeholder={braveKeySet ? "Terkonfigurasi (kosongkan jika tidak ingin mengubah)" : "Masukkan Brave API Key"}
                    value={braveKeyInput}
                    disabled={!loaded || savingKey}
                    onChange={(e) => setBraveKeyInput(e.target.value)}
                  />
                  <Button
                    type="button"
                    onClick={saveBraveKey}
                    disabled={!loaded || savingKey || braveKeyInput.trim() === ""}
                  >
                    Simpan
                  </Button>
                </div>
                {braveNeedsKey && (
                  <p className="text-xs text-amber-500">
                    Brave sudah dipilih tetapi Key belum dikonfigurasi, tool pencarian tidak akan aktif sebelum Key
                    disimpan.
                  </p>
                )}
                <p className="text-muted-foreground text-xs">
                  Kuota versi gratis sekitar 2.000 kali/bulan. Kunjungi https://brave.com/search/api/ untuk
                  mendapatkan Key.
                </p>
              </div>
            )}

            {webSearch && backend === "tavily" && (
              <div className="flex flex-col gap-2">
                <Label htmlFor="tavily-key" className="text-sm font-normal text-muted-foreground">
                  Tavily Search API Key
                  {tavilyKeySet && <span className="ml-2 text-xs text-emerald-500">Terkonfigurasi</span>}
                </Label>
                <div className="flex items-center gap-2">
                  <Input
                    id="tavily-key"
                    type="password"
                    autoComplete="off"
                    placeholder={tavilyKeySet ? "Terkonfigurasi (kosongkan jika tidak ingin mengubah)" : "Masukkan Tavily API Key (tvly-…)"}
                    value={tavilyKeyInput}
                    disabled={!loaded || savingTavilyKey}
                    onChange={(e) => setTavilyKeyInput(e.target.value)}
                  />
                  <Button
                    type="button"
                    onClick={saveTavilyKey}
                    disabled={!loaded || savingTavilyKey || tavilyKeyInput.trim() === ""}
                  >
                    Simpan
                  </Button>
                </div>
                {webSearch && backend === "tavily" && !tavilyKeySet && (
                  <p className="text-xs text-amber-500">
                    Tavily sudah dipilih tetapi Key belum dikonfigurasi, tool pencarian tidak akan aktif sebelum Key
                    disimpan.
                  </p>
                )}
                <p className="text-muted-foreground text-xs">
                  Kunjungi https://tavily.com untuk mendaftar dan mendapatkan API Key.
                </p>
              </div>
            )}

            {webSearch && (
              <div className="flex flex-col gap-2">
                <Label htmlFor="ws-proxy" className="text-sm font-normal text-muted-foreground">
                  Proxy Keluar (opsional)
                </Label>
                <div className="flex items-center gap-2">
                  <Input
                    id="ws-proxy"
                    autoComplete="off"
                    placeholder="http://host:port atau socks5://host:port (kosongkan = koneksi langsung)"
                    value={proxyInput}
                    disabled={!loaded || savingProxy}
                    onChange={(e) => setProxyInput(e.target.value)}
                  />
                  <Button type="button" onClick={saveProxy} disabled={!loaded || savingProxy}>
                    Simpan
                  </Button>
                </div>
                <p className="text-muted-foreground text-xs">
                  Proxy keluar terpisah, hanya dipakai untuk mengakses endpoint pencarian (VPN/SOCKS, dll). Tidak
                  terkait dengan proxy MITM pencatat trafik; dipakai untuk mengakses saat koneksi jaringan terputus.
                </p>
              </div>
            )}

            {webSearch && (
              <div className="flex items-center justify-between gap-4 border-t pt-4">
                <p className="text-muted-foreground text-xs">
                  Menjalankan satu kali pencarian "test" sungguhan dengan konfigurasi saat ini (sumber + proxy + Key)
                  untuk memverifikasi apakah berfungsi.
                </p>
                <Button
                  type="button"
                  variant="outline"
                  onClick={runTest}
                  disabled={!loaded || testing}
                  className="shrink-0"
                >
                  {testing ? "Sedang menguji…" : "Tes Pencarian"}
                </Button>
              </div>
            )}
          </CardContent>
        </Card>

        <Card className="mb-4 break-inside-avoid md:mb-6">
          <CardHeader>
            <CardTitle className="flex items-center gap-2 text-base">
              <RadioTowerIcon className="size-4" />
              Script Kustom · Interpreter Python
            </CardTitle>
            <CardDescription>
              Dipakai oleh tool bertipe <b>script</b> kustom untuk menjalankan Python. Terdeteksi otomatis saat
              startup (python3 diprioritaskan); di sini bisa diisi manual dengan path absolut venv / versi tertentu,
              kosongkan untuk deteksi otomatis saat runtime.
            </CardDescription>
          </CardHeader>
          <CardContent className="flex flex-col gap-3">
            <div className="flex items-center gap-2">
              <Input
                className="font-mono text-sm"
                placeholder="/usr/bin/python3 (kosongkan = deteksi otomatis)"
                value={pyInterp}
                disabled={!loaded || saving}
                onChange={(e) => setPyInterp(e.target.value)}
              />
              <Button variant="outline" onClick={detectPython} disabled={!loaded || saving}>
                Deteksi Ulang
              </Button>
              <Button onClick={savePython} disabled={!loaded || saving}>
                Simpan
              </Button>
            </div>
          </CardContent>
        </Card>

        <Card className="mb-4 break-inside-avoid md:mb-6">
          <CardHeader>
            <CardTitle className="flex items-center gap-2 text-base">
              <CpuIcon className="size-4" />
              Konkurensi Kerja · Jumlah Work Agent
            </CardTitle>
            <CardDescription>
              Jumlah work agent yang berjalan paralel untuk setiap tugas (default 3). Semakin besar nilainya, semakin
              banyak eksplorasi paralel dan semakin tinggi pula konsumsinya. Setelah diubah,{" "}
              <b>berlaku untuk tugas yang dimulai setelah ini</b>, tugas yang sedang berjalan tidak terpengaruh.
            </CardDescription>
          </CardHeader>
          <CardContent className="flex flex-col gap-3">
            <div className="flex items-center gap-2">
              <Input
                type="number"
                min={1}
                className="w-32 font-mono text-sm"
                placeholder="3"
                value={workers}
                disabled={!loaded || savingWorkers}
                onChange={(e) => setWorkers(e.target.value)}
              />
              <Button onClick={saveWorkers} disabled={!loaded || savingWorkers}>
                Simpan
              </Button>
            </div>
          </CardContent>
        </Card>

        <Card className="mb-4 break-inside-avoid md:mb-6">
          <CardHeader>
            <CardTitle className="flex items-center gap-2 text-base">
              <KeyboardIcon className="size-4" />
              Tombol Pengiriman Kotak Input Sesi
            </CardTitle>
            <CardDescription>
              Pengaturan ini dipakai bersama oleh kotak input sesi di halaman percakapan dan sesi Agent utama pada
              detail tugas, langsung berlaku setelah dipilih tanpa perlu disimpan.
              <br />
              Preferensi ini <b>hanya tersimpan di browser ini</b>, tidak disinkronkan dengan akun, perlu diatur
              ulang jika berganti browser atau membersihkan data situs.
            </CardDescription>
          </CardHeader>
          <CardContent className="flex items-center justify-between gap-4">
            <Label htmlFor="chat-send-mode" className="text-sm font-normal text-muted-foreground">
              Cara Pengiriman
            </Label>
            <Select value={sendMode} onValueChange={(v) => setChatSendMode(v as ChatSendMode)}>
              <SelectTrigger id="chat-send-mode" className="w-72">
                <SelectValue />
              </SelectTrigger>
              <SelectContent>
                {CHAT_SEND_MODE_OPTIONS.map((option) => (
                  <SelectItem key={option.value} value={option.value}>
                    {option.label}
                  </SelectItem>
                ))}
              </SelectContent>
            </Select>
          </CardContent>
        </Card>
      </div>
    </div>
  );
}
