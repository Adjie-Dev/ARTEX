"use client";

import * as React from "react";

import {
  BuildingIcon,
  ChevronRightIcon,
  CircleDashedIcon,
  GlobeIcon,
  LayoutTemplateIcon,
  LinkIcon,
  type LucideIcon,
  NetworkIcon,
  RefreshCwIcon,
  SearchIcon,
  SmartphoneIcon,
} from "lucide-react";

import { Button } from "@/components/ui/button";
import { InputGroup, InputGroupAddon, InputGroupInput } from "@/components/ui/input-group";
import type { FindingAssetKind, FindingAssetNode } from "@/lib/types";
import { cn } from "@/lib/utils";

// Ikon mengikuti pemetaan tipe dari halaman aset, aset yang sama akan tampak sama di kedua tempat.
const KIND_ICON: Record<FindingAssetKind, LucideIcon> = {
  company: BuildingIcon,
  root_domain: GlobeIcon,
  subdomain: GlobeIcon,
  ip: NetworkIcon,
  app: SmartphoneIcon,
  service: LayoutTemplateIcon,
  endpoint: LinkIcon,
  none: CircleDashedIcon,
};

const KIND_LABEL: Record<FindingAssetKind, string> = {
  company: "Perusahaan",
  root_domain: "Domain Utama",
  subdomain: "Subdomain",
  ip: "IP",
  app: "Aplikasi",
  service: "Layanan",
  endpoint: "Endpoint",
  none: "Tidak Terkait",
};

// TreeNode adalah tree yang dirakit dari array node. Backend sudah mengurutkan "yang temuannya lebih banyak
// di bawah induk yang sama ditaruh duluan", di sini cukup dipasang sesuai urutan array.
interface TreeNode extends FindingAssetNode {
  children: TreeNode[];
  depth: number;
  /** Teks yang benar-benar dirender di tree; label lengkap tetap disimpan di label (dipakai untuk tooltip dan breadcrumb). */
  display: string;
}

function stripBrackets(host: string) {
  return host.startsWith("[") && host.endsWith("]") ? host.slice(1, -1) : host;
}

function parseAssetURL(raw: string): URL | null {
  try {
    return new URL(raw);
  } catch {
    return null;
  }
}

// hostOf mengambil host yang diwakili sebuah node: untuk URL diambil hostname, untuk "host:port" diambil host,
// sisanya ya label itu sendiri (domain utama / subdomain / IP).
function hostOf(label: string): string {
  const url = parseAssetURL(label);
  if (url) return stripBrackets(url.hostname);
  const hostPort = label.match(/^(.+):(\d+)$/);
  return stripBrackets(hostPort ? hostPort[1] : label);
}

// shortLabel menghapus prefix yang sama dengan node induk. Label service / endpoint adalah URL lengkap,
// sedangkan domain/IP host-nya sudah ditulis di baris sebelumnya, node yang dalam sudah sempit, kalau host-nya
// diulang lagi, port dan path yang sebenarnya informatif jadi ikut terpotong. Nilai lengkap tetap ada di title dan breadcrumb.
function shortLabel(node: FindingAssetNode, parent?: FindingAssetNode): string {
  if (!parent) return node.label;

  // Subdomain menggantung di bawah domain utama: hapus akhiran domain utama, sisakan bagiannya sendiri.
  if (node.kind === "subdomain" && node.label.endsWith(`.${parent.label}`)) {
    return node.label.slice(0, -(parent.label.length + 1)) || node.label;
  }
  if (node.kind !== "service" && node.kind !== "endpoint") return node.label;

  // Label induk persis menjadi prefix dirinya sendiri (endpoint menggantung di bawah service dengan URL yang
  // sama, service menggantung di bawah IP yang sama): langsung dipotong.
  if (node.label.startsWith(parent.label)) {
    return node.label.slice(parent.label.length) || node.label;
  }

  // Kalau tidak, disingkat hanya jika node induk memang benar-benar host dari URL ini, kalau tidak informasi
  // pembeda akan hilang (misalnya service menggantung langsung di domain utama karena tidak ada baris aset
  // subdomain, maka harus ditampilkan URL lengkap).
  if (hostOf(node.label) !== hostOf(parent.label)) return node.label;

  const url = parseAssetURL(node.label);
  if (!url) return node.label;
  if (node.kind === "endpoint") return `${url.pathname}${url.search}` || "/";
  const scheme = url.protocol.replace(":", "");
  const port = url.port || (url.protocol === "https:" ? "443" : "80");
  return `${scheme} :${port}`;
}

export function buildAssetTree(nodes: FindingAssetNode[]): TreeNode[] {
  const byKey = new Map<string, TreeNode>();
  for (const node of nodes) {
    byKey.set(node.key, { ...node, children: [], depth: 0, display: node.label });
  }
  const roots: TreeNode[] = [];
  for (const node of nodes) {
    const current = byKey.get(node.key);
    if (!current) continue;
    const parent = node.parent ? byKey.get(node.parent) : undefined;
    // Jika node induk hilang (terbuang karena pemotongan level), dinaikkan jadi level teratas, supaya subtree tidak hilang seluruhnya.
    if (parent) {
      parent.children.push(current);
      current.display = shortLabel(node, parent);
    } else {
      roots.push(current);
    }
  }
  const setDepth = (node: TreeNode, depth: number) => {
    node.depth = depth;
    for (const child of node.children) setDepth(child, depth + 1);
  };
  for (const root of roots) setDepth(root, 0);
  return roots;
}

// assetPathOf mengembalikan jalur dari level teratas ke node ini, dipakai untuk breadcrumb di sebelah kanan.
// Setiap level juga hanya menampilkan selisih relatif terhadap level sebelumnya (display), nilai lengkap tetap di label.
export function assetPathOf(nodes: FindingAssetNode[], key: string | null): (FindingAssetNode & { display: string })[] {
  if (!key) return [];
  const byKey = new Map(nodes.map((n) => [n.key, n]));
  const path: FindingAssetNode[] = [];
  const seen = new Set<string>();
  let current = byKey.get(key);
  while (current && !seen.has(current.key)) {
    seen.add(current.key);
    path.unshift(current);
    current = current.parent ? byKey.get(current.parent) : undefined;
  }
  return path.map((node, index) => ({ ...node, display: shortLabel(node, path[index - 1]) }));
}

// filterTree menyaring berdasarkan kata kunci: node yang cocok dipertahankan, beserta seluruh rantai leluhurnya
// (leluhurnya sendiri boleh tidak cocok). Keturunan dari node yang cocok juga ikut dipertahankan, supaya bisa terus di-drill-down.
function filterTree(nodes: TreeNode[], keyword: string): TreeNode[] {
  const kw = keyword.trim().toLowerCase();
  if (!kw) return nodes;
  const walk = (node: TreeNode): TreeNode | null => {
    const hit = node.label.toLowerCase().includes(kw);
    if (hit) return node;
    const children = node.children.map(walk).filter((c): c is TreeNode => c !== null);
    if (children.length === 0) return null;
    return { ...node, children };
  };
  return nodes.map(walk).filter((n): n is TreeNode => n !== null);
}

// collectKeys mengumpulkan semua key dalam sebuah (sub)tree, dipakai untuk "perluas semua hasil yang cocok".
function collectKeys(nodes: TreeNode[], out: Set<string> = new Set()): Set<string> {
  for (const node of nodes) {
    out.add(node.key);
    collectKeys(node.children, out);
  }
  return out;
}

interface AssetTreeProps {
  nodes: FindingAssetNode[];
  selected: string | null;
  onSelect: (key: string | null) => void;
  loading?: boolean;
  truncated?: boolean;
  droppedKinds?: string[];
  /** Total temuan yang ditampilkan di sebelah kanan saat tidak ada aset yang dipilih, dipakai untuk baris "Semua Aset". */
  findingTotal: number;
  /** Tampilan aset tidak melakukan polling, hitungan tree diperbarui lewat tombol ini atau lewat tambah/hapus/ubah di dalam halaman. */
  onRefresh?: () => void;
}

export function AssetTree({
  nodes,
  selected,
  onSelect,
  loading,
  truncated,
  droppedKinds,
  findingTotal,
  onRefresh,
}: AssetTreeProps) {
  const [keyword, setKeyword] = React.useState("");
  const [expanded, setExpanded] = React.useState<Set<string>>(() => new Set());
  // Mengingat node yang pernah ditutup manual oleh pengguna, supaya "level teratas terbuka secara default" tidak membukanya lagi setelah setiap refresh.
  const [collapsed, setCollapsed] = React.useState<Set<string>>(() => new Set());

  const roots = React.useMemo(() => buildAssetTree(nodes), [nodes]);
  const visible = React.useMemo(() => filterTree(roots, keyword), [roots, keyword]);

  // Saat mencari, semua cabang yang cocok dibuka sekaligus, kalau tidak hasil yang cocok tersembunyi di node yang tertutup sama saja seperti tidak ketemu.
  const searching = keyword.trim() !== "";
  const searchKeys = React.useMemo(() => (searching ? collectKeys(visible) : null), [searching, visible]);

  const isExpanded = React.useCallback(
    (node: TreeNode) => {
      if (searchKeys) return searchKeys.has(node.key);
      if (expanded.has(node.key)) return true;
      // Level teratas terbuka satu tingkat secara default: level yang lebih dalam harus dibuka sendiri oleh pengguna, supaya tidak langsung menampilkan ribuan baris sekaligus.
      return node.depth === 0 && !collapsed.has(node.key);
    },
    [collapsed, expanded, searchKeys],
  );

  const toggle = React.useCallback(
    (node: TreeNode) => {
      const open = isExpanded(node);
      setExpanded((prev) => {
        const next = new Set(prev);
        if (open) next.delete(node.key);
        else next.add(node.key);
        return next;
      });
      setCollapsed((prev) => {
        const next = new Set(prev);
        if (open) next.add(node.key);
        else next.delete(node.key);
        return next;
      });
    },
    [isExpanded],
  );

  let emptyHint = "Tidak ada temuan yang terkait aset pada filter saat ini.";
  if (loading) emptyHint = "Memuat...";
  else if (searching) emptyHint = "Tidak ada aset yang cocok.";

  const rows: React.ReactNode[] = [];
  const pushRows = (list: TreeNode[]) => {
    for (const node of list) {
      const open = isExpanded(node);
      rows.push(
        <AssetTreeRow
          key={node.key}
          node={node}
          open={open}
          selected={selected === node.key}
          onToggle={() => toggle(node)}
          onSelect={() => onSelect(selected === node.key ? null : node.key)}
        />,
      );
      if (open && node.children.length > 0) pushRows(node.children);
    }
  };
  pushRows(visible);

  return (
    <div className="flex min-h-0 flex-1 flex-col gap-2">
      <div className="flex items-center gap-1">
        <InputGroup className="flex-1">
          <InputGroupInput
            type="search"
            value={keyword}
            onChange={(event) => setKeyword(event.target.value)}
            placeholder="Filter aset"
            aria-label="Filter aset"
          />
          <InputGroupAddon>
            <SearchIcon aria-hidden="true" />
          </InputGroupAddon>
        </InputGroup>
        {onRefresh && (
          <Button
            size="icon"
            variant="ghost"
            className="size-8 shrink-0 text-muted-foreground"
            onClick={onRefresh}
            disabled={loading}
            aria-label="Segarkan pohon aset"
            title="Segarkan pohon aset"
          >
            <RefreshCwIcon className={cn("size-4", loading && "animate-spin")} />
          </Button>
        )}
      </div>

      <button
        type="button"
        onClick={() => onSelect(null)}
        className={cn(
          "flex items-center justify-between gap-2 rounded-md px-2 py-1.5 text-left text-sm",
          selected === null ? "bg-accent font-medium" : "hover:bg-accent/50",
        )}
      >
        <span>Semua Aset</span>
        <span className="text-xs tabular-nums text-muted-foreground">{findingTotal}</span>
      </button>

      <div className="max-h-[24rem] min-h-0 flex-1 overflow-y-auto pr-2 lg:max-h-[calc(100vh-16rem)]">
        <div className="flex flex-col">
          {rows}
          {rows.length === 0 && <p className="px-2 py-8 text-center text-xs text-muted-foreground">{emptyHint}</p>}
        </div>
      </div>

      {truncated && (
        <p className="px-1 text-xs text-muted-foreground">
          Aset terlalu banyak, level {(droppedKinds ?? []).map((k) => KIND_LABEL[k as FindingAssetKind] ?? k).join(" / ")}
          {" "}disembunyikan (hitungannya tetap masuk ke level atasnya). Gunakan kotak filter untuk mempersempit dan melihat level lengkap.
        </p>
      )}
    </div>
  );
}

function AssetTreeRow({
  node,
  open,
  selected,
  onToggle,
  onSelect,
}: {
  node: TreeNode;
  open: boolean;
  selected: boolean;
  onToggle: () => void;
  onSelect: () => void;
}) {
  const Icon = KIND_ICON[node.kind] ?? GlobeIcon;
  const hasChildren = node.children.length > 0;
  return (
    <div
      className={cn(
        "group flex items-center gap-1 rounded-md pr-1 text-sm",
        selected ? "bg-accent" : "hover:bg-accent/50",
      )}
      style={{ paddingLeft: `${node.depth * 10}px` }}
    >
      {hasChildren ? (
        <button
          type="button"
          onClick={onToggle}
          className="flex size-5 shrink-0 items-center justify-center rounded text-muted-foreground hover:text-foreground"
          aria-label={open ? "Tutup" : "Buka"}
          aria-expanded={open}
        >
          <ChevronRightIcon className={cn("size-3.5 transition-transform", open && "rotate-90")} />
        </button>
      ) : (
        <span className="size-5 shrink-0" />
      )}
      <button
        type="button"
        onClick={onSelect}
        className="flex min-w-0 flex-1 items-center gap-1.5 py-1 text-left"
        title={`${KIND_LABEL[node.kind] ?? node.kind} · ${node.label}`}
      >
        <Icon className="size-3.5 shrink-0 text-muted-foreground" aria-hidden="true" />
        <span className={cn("min-w-0 truncate", selected && "font-medium")}>{node.display}</span>
      </button>
      <span className="flex shrink-0 items-center gap-1 text-xs tabular-nums">
        {node.critical > 0 && (
          <span className="text-rose-600" title={`Kritis ${node.critical}`}>
            {node.critical}
          </span>
        )}
        {node.high > 0 && (
          <span className="text-red-500" title={`Tinggi ${node.high}`}>
            {node.high}
          </span>
        )}
        <span className="text-muted-foreground" title={`Total ${node.total} temuan`}>
          {node.total}
        </span>
      </span>
    </div>
  );
}
