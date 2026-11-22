"use client";

import * as React from "react";

import { BellIcon, PlusIcon, SendIcon, Trash2Icon } from "lucide-react";
import { toast } from "sonner";

import { Badge } from "@/components/ui/badge";
import { Button } from "@/components/ui/button";
import { Card, CardContent, CardHeader, CardTitle } from "@/components/ui/card";
import { Input } from "@/components/ui/input";
import { Label } from "@/components/ui/label";
import { Select, SelectContent, SelectItem, SelectTrigger, SelectValue } from "@/components/ui/select";
import { Sheet, SheetContent, SheetDescription, SheetHeader, SheetTitle } from "@/components/ui/sheet";
import { Switch } from "@/components/ui/switch";
import { Tabs, TabsContent, TabsList, TabsTrigger } from "@/components/ui/tabs";
import { Textarea } from "@/components/ui/textarea";
import { api } from "@/lib/api";
import type { NotificationChannel, NotificationFilter, NotificationMeta } from "@/lib/types";

import {
  CHANNEL_FIELDS,
  type ChannelForm,
  emptyForm,
  KIND_LABEL,
  parseIDs,
  parseKeywords,
  parseKV,
  SEVERITY_OPTIONS,
} from "./_components/channel-fields";
import { ConfigField, FilterSummary } from "./_components/channel-form";
import { DeliveryList } from "./_components/delivery-list";
import { formatBacklog, StatTile } from "./_components/stat-tile";

// Halaman ini hanya bertugas mengatur: memuat data, menjaga state form, memanggil API.
// Definisi dan parsing field ada di _components/channel-fields.ts, kontrol dan ringkasan filter ada di
// _components/channel-form.tsx, catatan pengiriman ada di _components/delivery-list.tsx.
// Dipisah karena masing-masing bisa dipahami sendiri-sendiri, sementara kalau digabung dalam satu file halaman ini mendekati 1100 baris.
export default function NotifyPage() {
  const [meta, setMeta] = React.useState<NotificationMeta | null>(null);
  const [channels, setChannels] = React.useState<NotificationChannel[]>([]);
  const [tab, setTab] = React.useState<"channels" | "deliveries">("channels");

  const [open, setOpen] = React.useState(false);
  const [editing, setEditing] = React.useState<NotificationChannel | null>(null);
  const [form, setForm] = React.useState<ChannelForm>(emptyForm("dingtalk"));
  const [saving, setSaving] = React.useState(false);
  const [testing, setTesting] = React.useState(false);

  const [globalSaving, setGlobalSaving] = React.useState(false);
  const [baseURL, setBaseURL] = React.useState("");
  const [digestMin, setDigestMin] = React.useState("");

  const load = React.useCallback(() => {
    api
      .notifyMeta()
      .then((m) => {
        setMeta(m);
        setBaseURL(m.public_base_url);
        setDigestMin(m.digest_interval_min);
      })
      .catch((e) => toast.error("Gagal memuat konfigurasi notifikasi: " + (e as Error).message));
    // Kegagalan memuat daftar channel harus dilaporkan: kalau gagal diam-diam, tampilannya jadi seperti "tidak ada channel sama sekali",
    // yang membuat user mengira konfigurasinya hilang, itu lebih membuat panik daripada langsung menampilkan error.
    api
      .notifyChannels()
      .then(setChannels)
      .catch((e) => toast.error("Gagal memuat daftar channel: " + (e as Error).message));
  }, []);
  React.useEffect(() => {
    load();
  }, [load]);

  function setF(patch: Partial<ChannelForm>) {
    setForm((f) => ({ ...f, ...patch }));
  }
  function setCfg(key: string, value: unknown) {
    setForm((f) => ({ ...f, config: { ...f.config, [key]: value } }));
  }

  function openAdd() {
    setEditing(null);
    setForm(emptyForm(meta?.kinds[0]?.kind ?? "dingtalk"));
    setOpen(true);
  }

  function openEdit(ch: NotificationChannel) {
    setEditing(ch);
    // filter di backend adalah struct Go, selalu diserialisasi jadi objek (tidak akan null), jadi tidak perlu fallback.
    const f = ch.filter;
    setForm({
      name: ch.name,
      kind: ch.kind,
      mode: ch.mode,
      enabled: ch.enabled,
      ratePerMin: String(ch.rate_per_min),
      // Kredensial di dalam config yang dikembalikan backend berupa nilai masked; dimasukkan apa adanya ke form, dan saat submit dikirim balik apa adanya,
      // backend akan mempertahankan nilai asli di database berdasarkan itu.
      config: { ...ch.config },
      minSeverity: f.min_severity ?? "",
      includeText: (f.vulnclass_include ?? []).join("\n"),
      excludeText: (f.vulnclass_exclude ?? []).join("\n"),
      taskIDsText: (f.task_ids ?? []).join(","),
      assetIDsText: (f.asset_ids ?? []).join(","),
      onStatusChange: f.on_status_change ?? false,
    });
    setOpen(true);
  }

  // buildConfig mengubah state form jadi config channel.
  //
  // Hanya ada satu aturan, untuk dua jenis nilai:
  //   - Nilai masked ("__masked__...") dikirim balik apa adanya -> backend membacanya sebagai "field ini tidak diubah, pertahankan nilai asli di database"
  //   - Selain itu selalu dikirim sesuai input user, string kosong berarti "kosongkan field ini"
  //
  // Alasan kredensial tidak diberi perlakuan khusus (misalnya "kalau kredensial dikosongkan, lewati saja") adalah karena itu akan membuat user **tidak bisa menghapus**
  // kunci yang salah diisi, karena tidak ada aksi di UI yang bisa menyatakan "saya mau menghapusnya". Dengan aturan sekarang,
  // mengosongkan input field sama artinya dengan mengosongkan field itu, maknanya tunggal dan user yang mengontrol.
  // Nilai masked tidak akan muncul di dalam input field (lihat ConfigField), jadi "ada isi di field" selalu berarti
  // "memang diisi sendiri oleh user".
  function buildConfig(): Record<string, unknown> {
    const defs = CHANNEL_FIELDS[form.kind] ?? [];
    const out: Record<string, unknown> = {};
    for (const d of defs) {
      const raw = form.config[d.key];
      if (d.kind === "switch") {
        out[d.key] = raw === true;
        continue;
      }
      if (typeof raw === "string" && raw.startsWith("__masked__")) {
        out[d.key] = raw;
        continue;
      }
      if (d.kind === "number") {
        const n = Number(raw);
        out[d.key] = Number.isFinite(n) && n > 0 ? n : 0;
        continue;
      }
      if (d.kind === "kv") {
        out[d.key] = parseKV(String(raw ?? ""));
        continue;
      }
      if (d.kind === "list") {
        out[d.key] = String(raw ?? "")
          .split(/[\s,，]+/)
          .map((s) => s.trim())
          .filter(Boolean);
        continue;
      }
      out[d.key] = String(raw ?? "").trim();
    }
    return out;
  }

  function buildFilter(): NotificationFilter {
    return {
      min_severity: form.minSeverity || undefined,
      vulnclass_include: parseKeywords(form.includeText),
      vulnclass_exclude: parseKeywords(form.excludeText),
      task_ids: parseIDs(form.taskIDsText),
      asset_ids: parseIDs(form.assetIDsText),
      on_status_change: form.onStatusChange,
    };
  }

  async function saveForm() {
    if (!form.name.trim()) {
      toast.error("Isi dulu nama channel");
      return;
    }
    setSaving(true);
    try {
      const payload = {
        name: form.name.trim(),
        kind: form.kind,
        mode: form.mode,
        enabled: form.enabled,
        config: buildConfig(),
        filter: buildFilter(),
        rate_per_min: form.ratePerMin.trim() === "" ? undefined : Number(form.ratePerMin),
      };
      if (editing) {
        await api.notifyUpdateChannel(editing.id, payload);
        toast.success("Tersimpan");
        setOpen(false);
      } else {
        await api.notifyCreateChannel(payload);
        toast.success("Channel ditambahkan");
        setOpen(false);
      }
      load();
    } catch (e) {
      toast.error("Gagal menyimpan: " + (e as Error).message);
    } finally {
      setSaving(false);
    }
  }

  async function testChannel() {
    if (!editing) return;
    setTesting(true);
    try {
      const r = await api.notifyTestChannel(editing.id);
      toast.success(`Pesan uji terkirim (${r.latency_ms} ms), silakan cek di grup`);
    } catch (e) {
      // Backend meneruskan error asli dari channel apa adanya, ini satu-satunya petunjuk untuk melacak masalah konfigurasi, jadi ditampilkan apa adanya.
      toast.error("Gagal menguji: " + (e as Error).message, { duration: 12000 });
    } finally {
      setTesting(false);
    }
  }

  async function removeChannel(ch: NotificationChannel) {
    try {
      await api.notifyDeleteChannel(ch.id);
      toast.success(`Dihapus: ${ch.name}`);
      setOpen(false);
      load();
    } catch (e) {
      toast.error("Gagal menghapus: " + (e as Error).message);
    }
  }

  async function toggleEnabled(ch: NotificationChannel) {
    try {
      await api.notifyUpdateChannel(ch.id, { enabled: !ch.enabled });
      load();
    } catch (e) {
      toast.error("Operasi gagal: " + (e as Error).message);
    }
  }

  async function toggleGlobal(on: boolean) {
    setGlobalSaving(true);
    try {
      await api.setSettings({ notify_enabled: on });
      setMeta((m) => (m ? { ...m, enabled: on } : m));
      toast.success(on ? "Notifikasi diaktifkan" : "Notifikasi dijeda");
    } catch (e) {
      toast.error("Operasi gagal: " + (e as Error).message);
    } finally {
      setGlobalSaving(false);
    }
  }

  async function saveGlobal() {
    setGlobalSaving(true);
    try {
      const patch: Record<string, unknown> = { notify_public_base_url: baseURL.trim() };
      const n = Number(digestMin);
      if (Number.isFinite(n) && n > 0) patch.notify_digest_interval_min = n;
      await api.setSettings(patch);
      toast.success("Tersimpan");
      load();
    } catch (e) {
      toast.error("Gagal menyimpan: " + (e as Error).message);
    } finally {
      setGlobalSaving(false);
    }
  }

  const fields = CHANNEL_FIELDS[form.kind] ?? [];
  const secretKeys = new Set(meta?.kinds.find((k) => k.kind === form.kind)?.secret_keys ?? []);
  const defaultRate = meta?.kinds.find((k) => k.kind === form.kind)?.default_rate_per_min ?? 0;

  return (
    <div className="flex flex-1 flex-col gap-4 md:gap-6">
      <div className="flex items-start justify-between gap-4">
        <div>
          <h1 className="text-xl font-semibold tracking-tight">Notifikasi Push</h1>
          <p className="text-muted-foreground text-sm">
            Push ke DingTalk / Feishu / WeChat Work dan channel lainnya saat Kerentanan ditemukan, setiap channel bisa punya waktu pengiriman dan aturan filter sendiri
          </p>
        </div>
        {meta && (
          // Pakai div, bukan label: Switch sudah punya aria-label sendiri, kalau dibungkus label lagi
          // tidak akan terhubung ke kontrol native apa pun, dan malah membuat teksnya terlihat seperti bisa diklik untuk toggle.
          <div className="flex shrink-0 items-center gap-2 text-sm">
            <span className="text-muted-foreground">Saklar Utama</span>
            <Switch
              checked={meta.enabled}
              disabled={globalSaving}
              onCheckedChange={toggleGlobal}
              aria-label="Saklar utama notifikasi"
            />
          </div>
        )}
      </div>

      {meta && (
        <div className="grid grid-cols-2 gap-3 sm:grid-cols-3 lg:grid-cols-5">
          <StatTile label="Channel" value={`${meta.stats.channels_on} / ${meta.stats.channels}`} hint="Aktif / Total" />
          <StatTile label="Terkirim Hari Ini" value={String(meta.stats.sent_today)} />
          <StatTile label="Menunggu Kirim" value={String(meta.stats.pending)} />
          <StatTile label="Gagal" value={String(meta.stats.failed)} tone={meta.stats.failed > 0 ? "red" : undefined} />
          <StatTile
            label="Backlog Tertua"
            value={formatBacklog(meta.stats.backlog_age_ms)}
            // Usia backlog jauh lebih berguna daripada jumlah backlog: backlog 3 item bisa berarti dari 3 detik sampai 3 jam.
            hint={meta.stats.backlog_age_ms > 5 * 60_000 ? "Notifikasi mungkin tersendat" : undefined}
            tone={meta.stats.backlog_age_ms > 5 * 60_000 ? "red" : undefined}
          />
        </div>
      )}

      <Card className="gap-3">
        <CardHeader>
          <CardTitle className="text-base">Pengaturan Global</CardTitle>
        </CardHeader>
        <CardContent className="grid gap-4 sm:grid-cols-2">
          <div className="grid gap-2">
            <Label htmlFor="n-base">URL Tautan Balik</Label>
            <Input
              id="n-base"
              placeholder="https://artex.example.com"
              value={baseURL}
              onChange={(e) => setBaseURL(e.target.value)}
            />
            <p className="text-muted-foreground text-xs">Alamat yang dituju oleh tombol "Lihat Detail" di pesan. Kosongkan untuk tidak menampilkan tombol.</p>
          </div>
          <div className="grid gap-2">
            <Label htmlFor="n-digest">Periode Ringkasan (menit)</Label>
            <Input
              id="n-digest"
              type="number"
              min={1}
              max={1440}
              placeholder="30"
              value={digestMin}
              onChange={(e) => setDigestMin(e.target.value)}
            />
            <p className="text-muted-foreground text-xs">Hanya berlaku untuk channel dengan mode "Ringkasan".</p>
          </div>
          <div className="sm:col-span-2">
            <Button onClick={saveGlobal} disabled={globalSaving}>
              Simpan Pengaturan Global
            </Button>
          </div>
        </CardContent>
      </Card>

      <Tabs value={tab} onValueChange={(v) => setTab(v as "channels" | "deliveries")} className="flex flex-col gap-4">
        <TabsList>
          <TabsTrigger value="channels">Channel</TabsTrigger>
          <TabsTrigger value="deliveries">Catatan Pengiriman</TabsTrigger>
        </TabsList>

        <TabsContent value="channels">
          <div className="grid grid-cols-1 gap-4 sm:grid-cols-2 lg:grid-cols-3">
            <button
              type="button"
              onClick={openAdd}
              className="text-foreground/70 border-foreground/70 hover:bg-muted/60 hover:shadow-sm flex min-h-[130px] flex-col items-center justify-center gap-2 rounded-xl border border-dashed transition"
            >
              <PlusIcon className="size-6" />
              <span className="text-sm">Tambah Channel</span>
            </button>

            {channels.map((ch) => (
              <Card
                key={ch.id}
                onClick={() => openEdit(ch)}
                className="hover:border-primary/60 cursor-pointer gap-3 transition hover:shadow-sm"
              >
                <CardHeader>
                  <div className="flex items-center gap-2">
                    <BellIcon className="text-muted-foreground size-4 shrink-0" />
                    <CardTitle className="truncate text-base">{ch.name}</CardTitle>
                    {/* Seluruh kartu bisa diklik (masuk ke mode edit), jadi dua kontrol ini masing-masing harus menahan sendiri event bubble-nya,
                        kalau tidak toggle/hapus akan ikut memicu mode edit. stopPropagation dipasang langsung di kontrolnya
                        sendiri, bukan dengan membungkus div: membungkus div akan membuat elemen statis yang "terlihat
                        bisa diinteraksi tapi tanpa role", yang memicu peringatan a11y sekaligus tidak masuk akal secara semantik. */}
                    <div className="ml-auto flex items-center gap-2">
                      <Switch
                        checked={ch.enabled}
                        onCheckedChange={() => toggleEnabled(ch)}
                        onClick={(e) => e.stopPropagation()}
                        aria-label="Aktifkan"
                      />
                      <Button
                        size="icon"
                        variant="outline"
                        aria-label="Hapus"
                        onClick={(e) => {
                          e.stopPropagation();
                          // void secara eksplisit membuang Promise: removeChannel sudah catch dan toast sendiri,
                          // di sini tidak perlu await (onClick bukan async).
                          void removeChannel(ch);
                        }}
                      >
                        <Trash2Icon className="text-destructive" />
                      </Button>
                    </div>
                  </div>
                </CardHeader>
                <CardContent className="grid gap-3">
                  <div className="flex flex-wrap items-center gap-2">
                    <Badge variant="outline">{KIND_LABEL[ch.kind] ?? ch.kind}</Badge>
                    <Badge variant="outline">{ch.mode === "digest" ? "Ringkasan" : "Real-time"}</Badge>
                    {!ch.enabled && <Badge variant="outline">Nonaktif</Badge>}
                  </div>
                  <FilterSummary filter={ch.filter} />
                </CardContent>
              </Card>
            ))}
          </div>
        </TabsContent>

        <TabsContent value="deliveries">
          <DeliveryList channels={channels} />
        </TabsContent>
      </Tabs>

      <Sheet open={open} onOpenChange={setOpen}>
        <SheetContent side="right" className="w-full data-[side=right]:sm:max-w-lg">
          <SheetHeader>
            <SheetTitle>{editing ? editing.name : "Tambah Channel Notifikasi"}</SheetTitle>
            <SheetDescription>
              {KIND_LABEL[form.kind] ?? form.kind}
              {defaultRate > 0 ? ` · Limit default ${defaultRate} pesan/menit` : " · Tanpa limit"}
            </SheetDescription>
          </SheetHeader>

          <div className="flex min-h-0 flex-1 flex-col overflow-y-auto px-4">
            <div className="grid gap-4 py-4">
              <div className="grid gap-2">
                <Label>Tipe Channel</Label>
                <Select
                  value={form.kind}
                  onValueChange={(v) => {
                    // Ganti tipe berarti ganti satu set field kredensial, konfigurasi lama tidak boleh ikut digabungkan.
                    setF({ kind: v, config: {} });
                  }}
                  disabled={!!editing}
                >
                  <SelectTrigger>
                    <SelectValue />
                  </SelectTrigger>
                  <SelectContent>
                    {(meta?.kinds ?? []).map((k) => (
                      <SelectItem key={k.kind} value={k.kind}>
                        {KIND_LABEL[k.kind] ?? k.kind}
                      </SelectItem>
                    ))}
                  </SelectContent>
                </Select>
                {editing && (
                  <p className="text-muted-foreground text-xs">
                    Tipe channel tidak bisa diubah, mengubah tipe berarti mengganti satu set kredensial, silakan buat channel baru.
                  </p>
                )}
              </div>

              <div className="grid gap-2">
                <Label htmlFor="n-name">Nama Channel</Label>
                <Input
                  id="n-name"
                  placeholder="Grup Tanggap Darurat / Grup Info Harian"
                  value={form.name}
                  onChange={(e) => setF({ name: e.target.value })}
                />
              </div>

              {fields.length === 0 ? (
                <p className="text-muted-foreground text-sm">
                  Form untuk channel ini belum didefinisikan (entri CHANNEL_FIELDS belum ada di frontend), lengkapi dulu lalu coba lagi.
                </p>
              ) : (
                fields.map((d) => (
                  <ConfigField
                    key={d.key}
                    def={d}
                    value={form.config[d.key]}
                    isSecret={secretKeys.has(d.key)}
                    onChange={(v) => setCfg(d.key, v)}
                  />
                ))
              )}

              <div className="grid gap-2">
                <Label>Waktu Pengiriman</Label>
                <Select value={form.mode} onValueChange={(v) => setF({ mode: v as "realtime" | "digest" })}>
                  <SelectTrigger>
                    <SelectValue />
                  </SelectTrigger>
                  <SelectContent>
                    <SelectItem value="realtime">Real-time . Setiap Kerentanan dikirim satu per satu</SelectItem>
                    <SelectItem value="digest">Ringkasan . Digabung jadi satu pesan per periode</SelectItem>
                  </SelectContent>
                </Select>
                <p className="text-muted-foreground text-xs">
                  Kalau mau "Tinggi real-time, sisanya ringkasan", buat dua channel: satu real-time dengan batas minimal Tinggi, satu ringkasan tanpa batas level.
                </p>
              </div>

              <div className="grid gap-2">
                <Label htmlFor="n-rate">Limit (pesan/menit)</Label>
                <Input
                  id="n-rate"
                  type="number"
                  min={0}
                  placeholder={defaultRate > 0 ? String(defaultRate) : "0 = tanpa limit"}
                  value={form.ratePerMin}
                  onChange={(e) => setF({ ratePerMin: e.target.value })}
                />
                <p className="text-muted-foreground text-xs">
                  Kosongkan untuk memakai nilai default channel; 0 berarti tanpa limit. Melebihi limit tidak akan membuang pesan, hanya menunda pengirimannya.
                </p>
              </div>

              <div className="border-t pt-4">
                <p className="mb-3 text-sm font-medium">Aturan Filter (kosongkan untuk tidak memfilter)</p>
                <div className="grid gap-4">
                  <div className="grid gap-2">
                    <Label>Level Minimum</Label>
                    <Select
                      value={form.minSeverity || "all"}
                      onValueChange={(v) => setF({ minSeverity: v === "all" ? "" : v })}
                    >
                      <SelectTrigger>
                        <SelectValue />
                      </SelectTrigger>
                      <SelectContent>
                        {SEVERITY_OPTIONS.map((o) => (
                          <SelectItem key={o.value || "all"} value={o.value || "all"}>
                            {o.label}
                          </SelectItem>
                        ))}
                      </SelectContent>
                    </Select>
                  </div>
                  <div className="grid gap-2">
                    <Label htmlFor="n-inc">Hanya push jenis Kerentanan ini</Label>
                    <Textarea
                      id="n-inc"
                      placeholder={"SQL Injection\nCommand Execution"}
                      value={form.includeText}
                      onChange={(e) => setF({ includeText: e.target.value })}
                    />
                    <p className="text-muted-foreground text-xs">
                      Satu keyword per baris, pencocokan substring tidak case-sensitive. Kosongkan = semua jenis.
                    </p>
                  </div>
                  <div className="grid gap-2">
                    <Label htmlFor="n-exc">Kecualikan jenis Kerentanan ini</Label>
                    <Textarea
                      id="n-exc"
                      placeholder={"Information Disclosure"}
                      value={form.excludeText}
                      onChange={(e) => setF({ excludeText: e.target.value })}
                    />
                    <p className="text-muted-foreground text-xs">Pengecualian lebih diprioritaskan daripada penyertaan: kalau keduanya cocok, akan dikecualikan.</p>
                  </div>
                  <div className="grid gap-2">
                    <Label htmlFor="n-tasks">Batasi ID Tugas</Label>
                    <Input
                      id="n-tasks"
                      placeholder="1, 2, 3"
                      value={form.taskIDsText}
                      onChange={(e) => setF({ taskIDsText: e.target.value })}
                    />
                  </div>
                  <div className="grid gap-2">
                    <Label htmlFor="n-assets">Batasi ID Aset</Label>
                    <Input
                      id="n-assets"
                      placeholder="10, 11"
                      value={form.assetIDsText}
                      onChange={(e) => setF({ assetIDsText: e.target.value })}
                    />
                    <p className="text-muted-foreground text-xs">Tugas/Aset dikosongkan = tanpa batasan; kalau diisi, Kerentanan harus beririsan dengan daftar ini.</p>
                  </div>
                  <div className="flex items-center gap-2 text-sm">
                    <Switch
                      checked={form.onStatusChange}
                      onCheckedChange={(v) => setF({ onStatusChange: v })}
                      aria-label="Terima perubahan status"
                    />
                    Push juga saat status penanganan Kerentanan berubah (hanya mode real-time)
                  </div>
                </div>
              </div>

              <div className="flex items-center gap-2 text-sm">
                <Switch checked={form.enabled} onCheckedChange={(v) => setF({ enabled: v })} aria-label="Aktifkan" />
                Aktifkan channel ini
              </div>
            </div>

            <div className="flex gap-2 pt-2 pb-6">
              <Button onClick={saveForm} disabled={saving}>
                {editing ? "Simpan" : "Tambah"}
              </Button>
              {editing && (
                <Button variant="outline" onClick={testChannel} disabled={testing}>
                  <SendIcon /> Kirim Pesan Uji
                </Button>
              )}
            </div>
          </div>
        </SheetContent>
      </Sheet>
    </div>
  );
}
