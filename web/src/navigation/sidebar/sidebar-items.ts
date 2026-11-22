import {
  Activity,
  Ban,
  BellRing,
  Bot,
  Brain,
  Bug,
  ClipboardList,
  FolderOpen,
  FolderSync,
  LayoutDashboard,
  type LucideIcon,
  MessageSquare,
  Network,
  Plug,
  Radio,
  ScrollText,
  Settings2,
  ShieldAlert,
  Sparkles,
  Target,
  Terminal,
  Wrench,
} from "lucide-react";

export type NavBadge = "new" | "soon";

export interface NavSubItem {
  id: string;
  title: string;
  url: string;
  icon?: LucideIcon;
  badge?: NavBadge;
  disabled?: boolean;
  newTab?: boolean;
}

interface NavItemBase {
  id: string;
  title: string;
  icon?: LucideIcon;
  badge?: NavBadge;
  disabled?: boolean;
  newTab?: boolean;
}

export interface NavMainLinkItem extends NavItemBase {
  url: string;
  subItems?: never;
}

export interface NavMainParentItem extends NavItemBase {
  subItems: NavSubItem[];
}

export type NavMainItem = NavMainLinkItem | NavMainParentItem;

export interface NavGroup {
  id: number;
  label?: string;
  items: NavMainItem[];
}

export const sidebarItems: NavGroup[] = [
  {
    id: 1,
    label: "Fungsi",
    items: [
      { id: "dashboard", title: "Dasbor", url: "/dashboard", icon: LayoutDashboard },
      { id: "chat", title: "Percakapan", url: "/chat", icon: MessageSquare },
      { id: "tasks", title: "Tugas", url: "/function/tasks", icon: Target },
      { id: "findings", title: "Temuan", url: "/function/findings", icon: Bug },
      { id: "traffic", title: "Trafik", url: "/function/traffic", icon: Activity },
      { id: "commands", title: "Eksekusi Tool", url: "/function/commands", icon: Terminal },
      { id: "llm-records", title: "Perekaman LLM", url: "/function/llm-records", icon: Radio },
      { id: "assets", title: "Aset", url: "/function/assets", icon: Network },
      { id: "sync", title: "Sinkronisasi Aset", url: "/function/sync", icon: FolderSync },
      { id: "workspace", title: "Workspace", url: "/function/workspace", icon: FolderOpen },
    ],
  },
  {
    id: 2,
    label: "Sistem",
    items: [
      { id: "llm", title: "LLM", url: "/system/llm", icon: Brain },
      { id: "agents", title: "Agent", url: "/system/agents", icon: Bot },
      { id: "mcp", title: "MCP", url: "/system/mcp", icon: Plug },
      { id: "skills", title: "Skill", url: "/system/skills", icon: Sparkles },
      { id: "tools", title: "Tool", url: "/system/tools", icon: Wrench },
      { id: "notify", title: "Notifikasi", url: "/system/notify", icon: BellRing },
      { id: "intercept", title: "Aturan Intersepsi", url: "/system/intercept", icon: ShieldAlert },
      { id: "asset-intercept", title: "Intersepsi Aset", url: "/system/intercept/assets", icon: Ban },
      { id: "approvals", title: "Catatan Persetujuan", url: "/system/intercept/approvals", icon: ClipboardList },
      { id: "logs", title: "Log", url: "/system/logs", icon: ScrollText },
      { id: "settings", title: "Konfigurasi Sistem", url: "/system/settings", icon: Settings2 },
    ],
  },
];
