// Tabel field channel dan utilitas parsing nilai konfigurasi.
//
// Dipisah dari halaman karena bagian ini adalah **data**, bukan tampilan: mendeskripsikan field apa yang dimiliki setiap channel,
// kontrol apa yang dipakai masing-masing, serta konversi dua arah antara teks form dan nilai konfigurasi (JSON).
// Dengan dipisah ke file sendiri, channel baru hanya perlu diubah di sini, halaman itu sendiri tidak perlu diubah.
// Nama tampilan dan deskripsi singkat untuk tiap tipe channel. Ditaruh di frontend karena hanya memengaruhi teks tampilan, backend tidak perlu tahu.
export const KIND_LABEL: Record<string, string> = {
  dingtalk: "DingTalk",
  feishu: "Feishu",
  wecom: "WeCom",
  webhook: "Webhook Umum",
  telegram: "Telegram",
  email: "Email",
};

// Definisi field konfigurasi untuk setiap channel.
//
// Tabel field frontend ini dipertahankan dengan sengaja, bukan membiarkan backend mengirim schema: backend hanya bertanggung jawab atas
// Validate (wajib isi/format), sementara UI membutuhkan layout dan tipe kontrol, keduanya memperhatikan hal yang berbeda.
// Satu-satunya titik keterkaitan adalah secret_keys, field mana yang harus dirender sebagai password box ditentukan oleh backend,
// karena hanya implementasi channel itu sendiri yang tahu pasti nilai mana yang merupakan kredensial (untuk WeCom, seluruh Webhook adalah
// kredensial, sedangkan untuk DingTalk hanya salah satu secret-nya). Saat channel baru ditambahkan, kurang satu entri di sini hanya akan
// membuat form menjadi kosong, bukan gagal secara senyap (hasFields di bawah akan memberi peringatan).
export type FieldKind = "text" | "password" | "number" | "select" | "textarea" | "switch" | "kv" | "list";
export interface FieldDef {
  key: string;
  label: string;
  kind: FieldKind;
  placeholder?: string;
  help?: string;
  options?: { value: string; label: string }[];
}
export const CHANNEL_FIELDS: Record<string, FieldDef[]> = {
  dingtalk: [
    {
      key: "webhook",
      label: "Alamat Webhook",
      kind: "text",
      placeholder: "https://oapi.dingtalk.com/robot/send?access_token=...",
    },
    {
      key: "secret",
      label: "Kunci Penandatanganan",
      kind: "password",
      help: "Isi jika pengaturan keamanan bot memilih \"Penandatanganan\"; kosongkan jika memilih \"Kata Kunci Kustom\" atau pengaturan keamanan belum diaktifkan",
    },
  ],
  feishu: [
    {
      key: "webhook",
      label: "Alamat Webhook",
      kind: "text",
      placeholder: "https://open.feishu.cn/open-apis/bot/v2/hook/...",
    },
    { key: "secret", label: "Kunci Verifikasi Tanda Tangan", kind: "password", help: "Isi jika bot mengaktifkan \"Verifikasi Tanda Tangan\", jika tidak biarkan kosong" },
  ],
  wecom: [
    {
      key: "webhook",
      label: "Alamat Webhook",
      kind: "text",
      placeholder: "https://qyapi.weixin.qq.com/cgi-bin/webhook/send?key=...",
    },
  ],
  webhook: [
    { key: "url", label: "URL Tujuan", kind: "text", placeholder: "https://your-endpoint.example.com/hook" },
    {
      key: "method",
      label: "Metode Request",
      kind: "select",
      options: [
        { value: "POST", label: "POST (dengan body request)" },
        { value: "PUT", label: "PUT (dengan body request)" },
        { value: "PATCH", label: "PATCH (dengan body request)" },
        { value: "GET", label: "GET (tanpa body request)" },
      ],
    },
    { key: "headers", label: "Header Request Kustom", kind: "kv", help: "Satu baris KEY=VALUE, contoh Authorization=Bearer xxx" },
    {
      key: "body_template",
      label: "Template Body Request",
      kind: "textarea",
      help:
        "Dikosongkan untuk memakai template bawaan. Variabel: {{.Title}} {{.Batch}} {{.Count}} {{.HomeURL}} {{.SentAt}}, " +
        "serta .Name/.VulnClass/.Severity/.Summary/.Assets/.DetailURL/.StatusLabel di bawah range .Items. " +
        "Untuk menyisipkan string gunakan {{json .Xxx}}, bukan {{.Xxx}}, karena tanda kutip di judul bisa merusak JSON.",
    },
  ],
  telegram: [
    { key: "bot_token", label: "Bot Token", kind: "password", placeholder: "123456:ABC-DEF..." },
    { key: "chat_id", label: "Chat ID", kind: "text", placeholder: "-1001234567890" },
    {
      key: "base_url",
      label: "Alamat API",
      kind: "text",
      placeholder: "https://api.telegram.org",
      help: "Dikosongkan untuk memakai alamat resmi; isi jika memakai reverse proxy Bot API sendiri",
    },
  ],
  email: [
    { key: "host", label: "Server SMTP", kind: "text", placeholder: "smtp.example.com" },
    {
      key: "port",
      label: "Port",
      kind: "number",
      placeholder: "587",
      help: "Port 587 memakai STARTTLS; untuk 465 aktifkan \"TLS Implisit\"",
    },
    { key: "username", label: "Akun", kind: "text" },
    { key: "password", label: "Kata Sandi / Kode Otorisasi", kind: "password" },
    { key: "from", label: "Pengirim", kind: "text", placeholder: "artex@example.com" },
    { key: "to", label: "Penerima", kind: "list", help: "Beberapa alamat dipisahkan dengan koma" },
    { key: "tls", label: "TLS Implisit", kind: "switch", help: "Aktifkan untuk port 465; biarkan nonaktif untuk 587 (akan otomatis STARTTLS)" },
  ],
};

export const SEVERITY_OPTIONS = [
  { value: "", label: "Tidak dibatasi" },
  { value: "low", label: "Rendah ke atas" },
  { value: "medium", label: "Sedang ke atas" },
  { value: "high", label: "Tinggi ke atas" },
  { value: "critical", label: "Hanya Kritis" },
];

export type ChannelForm = {
  name: string;
  kind: string;
  mode: "realtime" | "digest";
  enabled: boolean;
  ratePerMin: string;
  config: Record<string, unknown>;
  minSeverity: string;
  includeText: string;
  excludeText: string;
  taskIDsText: string;
  assetIDsText: string;
  onStatusChange: boolean;
};

export const emptyForm = (kind: string): ChannelForm => ({
  name: "",
  kind,
  mode: "realtime",
  enabled: true,
  ratePerMin: "",
  config: {},
  minSeverity: "",
  includeText: "",
  excludeText: "",
  taskIDsText: "",
  assetIDsText: "",
  onStatusChange: false,
});

// parseKV mem-parsing textarea berformat "satu baris KEY=VALUE".
export function parseKV(text: string): Record<string, string> {
  const out: Record<string, string> = {};
  for (const line of text.split("\n")) {
    const t = line.trim();
    if (!t) continue;
    const i = t.indexOf("=");
    if (i > 0) out[t.slice(0, i).trim()] = t.slice(i + 1).trim();
  }
  return out;
}
// parseIDs mem-parsing daftar id yang dipisahkan koma/spasi.
export function parseIDs(text: string): number[] {
  return text
    .split(/[\s,，]+/)
    .map((s) => s.trim())
    .filter(Boolean)
    .map((s) => Number(s))
    .filter((n) => Number.isFinite(n) && n > 0);
}
// parseKeywords mem-parsing daftar keyword yang dipisahkan baris/koma (nama tipe kerentanan bisa mengandung spasi, karena itu dipotong berdasarkan baris atau koma).
export function parseKeywords(text: string): string[] {
  return text
    .split(/[\n,，]+/)
    .map((s) => s.trim())
    .filter(Boolean);
}
