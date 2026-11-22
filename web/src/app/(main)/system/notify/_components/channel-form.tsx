"use client";

import { CheckIcon } from "lucide-react";

import { Input } from "@/components/ui/input";
import { Label } from "@/components/ui/label";
import { Select, SelectContent, SelectItem, SelectTrigger, SelectValue } from "@/components/ui/select";
import { Switch } from "@/components/ui/switch";
import { Textarea } from "@/components/ui/textarea";
import type { NotificationFilter } from "@/lib/types";

// asText / inputType adalah helper pengambilan nilai khusus file ini (terkait erat dengan rendering kontrol), tidak diletakkan di channel-fields.
import { type FieldDef, type FieldKind, SEVERITY_OPTIONS } from "./channel-fields";

// asText merender nilai konfigurasi apa pun menjadi string yang bisa dipakai input box.
// config berasal dari JSON, nilainya bisa berupa string / number / boolean / array / null,
// di sini yang dipedulikan hanya "bisa tidaknya dimasukkan ke text box", serialisasi sesungguhnya ditangani oleh buildConfig.
function asText(v: unknown): string {
  if (typeof v === "string") return v;
  if (v === null || v === undefined) return "";
  return String(v);
}

// inputType memetakan tipe field ke atribut type pada input.
function inputType(kind: FieldKind): "text" | "password" | "number" {
  if (kind === "password") return "password";
  if (kind === "number") return "number";
  return "text";
}

// ConfigField merender kontrol sesuai definisi field.
//
// Penanganan field bermask adalah satu-satunya hal yang perlu diperhatikan di sini: input box **tidak
// menampilkan** nilai mask itu sendiri, hanya menampilkan satu baris keterangan "sudah tersimpan". Dengan begitu
// hanya ada satu aturan di UI, yaitu ada teks di kotak berarti diisi user, kotak kosong berarti nilai kosong.
// Kalau "__masked__:…abc123" dimasukkan ke input box, user akan mengira itu teks placeholder yang harus dihapus
// sendiri, malah lebih gampang salah hapus kredensial.
export function ConfigField({
  def,
  value,
  isSecret,
  onChange,
}: {
  def: FieldDef;
  value: unknown;
  isSecret: boolean;
  onChange: (v: unknown) => void;
}) {
  const id = `n-cfg-${def.key}`;
  const raw = asText(value);
  // Nilai mask yang dikembalikan backend: berbentuk "__masked__:…abc123", bagian ekornya adalah potongan nilai asli yang bisa dikenali.
  const masked = isSecret && raw.startsWith("__masked__");
  const maskedTail = masked ? (raw.split("…")[1] ?? "") : "";

  if (def.kind === "switch") {
    return (
      <div className="flex items-center gap-2 text-sm">
        <Switch checked={value === true} onCheckedChange={onChange} aria-label={def.label} />
        {def.label}
        {def.help && <span className="text-muted-foreground">（{def.help}）</span>}
      </div>
    );
  }

  if (def.kind === "select") {
    return (
      <div className="grid gap-2">
        <Label>{def.label}</Label>
        <Select value={raw || def.options?.[0]?.value} onValueChange={onChange}>
          <SelectTrigger>
            <SelectValue />
          </SelectTrigger>
          <SelectContent>
            {(def.options ?? []).map((o) => (
              <SelectItem key={o.value} value={o.value}>
                {o.label}
              </SelectItem>
            ))}
          </SelectContent>
        </Select>
      </div>
    );
  }

  // Kontrol dipilih berdasarkan tipe field. Memakai rantai if, bukan ternary bersarang, karena di sini perlu
  // membedakan empat jenis kontrol, ternary tiga lapis sudah bikin pusing menghitung kurung saat dibaca.
  function control() {
    if (def.kind === "textarea" || def.kind === "kv") {
      return (
        <Textarea
          id={id}
          className="font-mono"
          placeholder={def.placeholder}
          value={masked ? "" : raw}
          onChange={(e) => onChange(e.target.value)}
        />
      );
    }
    if (def.kind === "list") {
      return (
        <Input
          id={id}
          value={Array.isArray(value) ? (value as string[]).join(", ") : raw}
          onChange={(e) => onChange(e.target.value)}
          placeholder={def.placeholder}
        />
      );
    }
    return (
      <Input
        id={id}
        className={def.kind === "text" ? "font-mono" : ""}
        type={inputType(def.kind)}
        placeholder={def.placeholder}
        value={masked ? "" : raw}
        onChange={(e) => onChange(e.target.value)}
      />
    );
  }

  const hint = masked ? (
    <p className="text-muted-foreground flex items-center gap-1 text-xs">
      <CheckIcon className="size-3" />
      Sudah tersimpan{maskedTail ? ` (akhiran ${maskedTail})` : ""} · isi nilai baru untuk menimpa, kosongkan untuk menghapus item ini
    </p>
  ) : (
    def.help && <p className="text-muted-foreground text-xs">{def.help}</p>
  );

  return (
    <div className="grid gap-2">
      <Label htmlFor={id}>{def.label}</Label>
      {control()}
      {hint}
    </div>
  );
}

// FilterSummary meringkas kondisi filter jadi satu baris, supaya kartu bisa menunjukkan apa yang dikirim channel ini tanpa perlu dibuka.
export function FilterSummary({ filter }: { filter: NotificationFilter }) {
  const parts: string[] = [];
  if (filter.min_severity) {
    parts.push(SEVERITY_OPTIONS.find((o) => o.value === filter.min_severity)?.label ?? filter.min_severity);
  }
  if (filter.vulnclass_include?.length) parts.push(`Berisi ${filter.vulnclass_include.length} jenis kata kunci`);
  if (filter.vulnclass_exclude?.length) parts.push(`Mengecualikan ${filter.vulnclass_exclude.length} kata kunci`);
  if (filter.task_ids?.length) parts.push(`${filter.task_ids.length} tugas`);
  if (filter.asset_ids?.length) parts.push(`${filter.asset_ids.length} aset`);
  if (filter.on_status_change) parts.push("Termasuk perubahan status");
  if (parts.length === 0) {
    return <p className="text-muted-foreground text-sm">Semua kerentanan</p>;
  }
  return <p className="text-muted-foreground text-sm">{parts.join(" · ")}</p>;
}
