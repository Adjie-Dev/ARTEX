import type { NewAssetType } from "@/lib/types";

const ASSET_TYPE_LABELS: Record<NewAssetType, string> = {
  app: "Aplikasi",
  endpoint: "Endpoint",
  ip: "IP",
  root_domain: "Root Domain",
  service: "Service",
  subdomain: "Subdomain",
};

const TASK_ASSET_SOURCE_LABELS: Record<string, string> = {
  agent: "Ditemukan Agent",
  anchor: "Anchor Blackboard",
  api: "API Aset",
  company: "Terkait Perusahaan",
  legacy: "Terkait Historis",
  manual: "Ditambahkan Manual",
  system: "Terkait Sistem",
  task: "Inisialisasi Tugas",
};

export function taskAssetTypeLabel(type: NewAssetType): string {
  return ASSET_TYPE_LABELS[type];
}

export function taskAssetSourceLabel(source: string): string {
  return TASK_ASSET_SOURCE_LABELS[source] ?? source;
}
