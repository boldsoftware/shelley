import { ref } from "vue";
import { apiJSON } from "./api";
import { workspaceMode } from "../utils/workspaceMode";

/** Workspace mode's sidebar has dev and prod tabs with the server's
 *  "dev-prod-tabs" feature flag on; read once per page load. */
export const devProdTabs =
  workspaceMode && typeof window !== "undefined" && window.__SHELLEY_INIT__?.dev_prod_tabs === true;

/** One of the user's exe.dev VMs, as the sidebar's prod tab lists it. */
export interface ExeVM {
  name: string;
  emoji: string;
  status: string;
  url: string;
  ssh_command: string;
  public: boolean;
  comment: string;
  tags: string[] | null;
  /** "base" for a project's base VM, "conversation" for a conversation's copy of one. */
  role: "" | "base" | "conversation";
  /** The exe.dev integration that lists the VM, which its settings use. */
  integration: string;
}

/** A VM's settings: exe.dev's `ls -l --json` record of it (its field
 *  names) and what Shelley adds. */
export interface ExeVMSettings {
  vm_name: string;
  emoji: string;
  status: string;
  region_display: string;
  image: string;
  pool_name: string;
  created_at: string;
  comment: string;
  tags: string[];
  https_url: string;
  ssh_command: string;
  shelley_url: string;
  terminal_url: string;
  allocated_cpus: number;
  memory_capacity_bytes: number;
  disk_capacity_bytes: number;
  proxy_port: number;
  proxy_share: string;
  sharing: {
    named_user_shares: { email: string; status: string; share_level: string }[] | null;
    team_shared: boolean;
  };
  share_links: { token: string; url: string }[] | null;
  domains: string[] | null;
  egress_policy: string;
  email_receive_enabled: boolean;
  email_address: string;
  integration: string;
  /** The only tag of VMs the integration reaches, or "". */
  scope_tag: string;
  role: ExeVM["role"];
  /** Why each change that can't be made here can't. */
  denied: Partial<Record<ExeVMChangeKind, string>>;
}

export type ExeVMChangeKind =
  | "comment"
  | "tag-add"
  | "tag-remove"
  | "port"
  | "public"
  | "private"
  | "share-add"
  | "share-remove"
  | "link-add"
  | "link-remove"
  | "domain-add"
  | "domain-remove"
  | "egress"
  | "email"
  | "resize"
  | "rename"
  | "restart"
  | "pause"
  | "resume"
  | "copy"
  | "delete";

/** One change of a VM's settings; resize takes the sizes (0 keeps one). */
export interface ExeVMChange {
  kind: ExeVMChangeKind;
  value?: string;
  cpus?: number;
  memory_gb?: number;
  disk_gb?: number;
}

/** Bumped by every change of a VM, so lists of VMs load afresh. */
export const exeVmsChanged = ref(0);

/** The prod tab's VMs; setup_url, when this host has no exe.dev integration
 *  to list them with, is where the user can attach one. */
export interface ExeVMList {
  vms: ExeVM[];
  setup_url?: string;
}

export const exeVmsApi = {
  /** Merged across this host's exe.dev integrations; cached briefly unless refresh. */
  list: (refresh: boolean) =>
    apiJSON<ExeVMList>(`/api/exe-vms${refresh ? "?refresh=1" : ""}`, "Failed to list exe.dev VMs"),
  /** Why name's page can't be framed on this page's origin, or "". */
  frameBlocked: (name: string) =>
    apiJSON<{ blocked: string }>(
      `/api/exe-vms/${encodeURIComponent(name)}/frame?origin=${encodeURIComponent(window.location.origin)}`,
      `Failed to check whether ${name} can be shown here`,
    ).then((r) => r.blocked),
  settings: (name: string) =>
    apiJSON<ExeVMSettings>(
      `/api/exe-vms/${encodeURIComponent(name)}/settings`,
      `Failed to read ${name}'s settings`,
    ),
  change: async (name: string, change: ExeVMChange) => {
    try {
      await apiJSON<void>(
        `/api/exe-vms/${encodeURIComponent(name)}/settings`,
        `Failed to change ${name}`,
        "POST",
        change,
      );
    } finally {
      exeVmsChanged.value++;
    }
  },
};

export type NavTab = "dev" | "prod";
const NAV_TAB_KEY = "shelley_nav_tab";

export function savedNavTab(): NavTab {
  return localStorage.getItem(NAV_TAB_KEY) === "prod" ? "prod" : "dev";
}

export function saveNavTab(tab: NavTab) {
  localStorage.setItem(NAV_TAB_KEY, tab);
}

/** A VM's view's tabs: its settings, first, its web page, and a shell on it. */
export type VmTab = "settings" | "page" | "terminal";

/** The VM name in a /vm/<name>[/page|/terminal] path, or null. */
export function vmFromPath(path: string): string | null {
  const m = path.match(/^\/vm\/([^/]+)(\/page|\/terminal)?\/?$/);
  return m ? decodeURIComponent(m[1]) : null;
}

/** The tab of a /vm/<name>[/page|/terminal] path. */
export function vmTabFromPath(path: string): VmTab {
  const m = path.match(/^\/vm\/[^/]+\/(page|terminal)\/?$/);
  return m ? (m[1] as VmTab) : "settings";
}

export function vmPath(name: string, tab: VmTab): string {
  return `/vm/${encodeURIComponent(name)}${tab === "settings" ? "" : `/${tab}`}`;
}
