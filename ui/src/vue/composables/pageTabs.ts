import { computed, onUnmounted, reactive, ref, watch, type Ref } from "vue";
import type { Message } from "../../types";
import type { TranslationKeys } from "../../i18n/types";
import { pageFrame, type FrameBlocked, type PageFrame } from "../../services/pageFrames";
import { errorMessage } from "../../utils/errorMessage";
import { localhostLinkOptionsFromInit, rewriteLocalhostLink } from "../../utils/linkify";
import { pageLinks, pageTabs, type PageLink, type PageTab } from "../../utils/pageTabs";
import { useI18n } from "./i18n";
import { useWorkspace } from "./workspace";

/** A page tab with whether its page can be framed: frame is null while
 *  checking. */
export interface PageTabView extends PageTab {
  frame: PageFrame | null;
  /** Why the browser can't frame it, worded, or "". */
  blocked: string;
  error: string;
  /** When it was last reloaded, or 0: a reload replaces the frame. */
  reloadedAt: number;
}

interface FrameState {
  frame: PageFrame | null;
  error: string;
}

const BLOCKED: Record<FrameBlocked, keyof TranslationKeys> = {
  shelley: "pageFrameBlockedShelley",
  csp: "pageFrameBlockedCsp",
  "x-frame-options": "pageFrameBlockedXFrameOptions",
  login: "pageFrameBlockedLogin",
};

// The closed tabs' URLs by conversation, the latest closed last, for the
// last CLOSED_CONVERSATIONS conversations that closed any.
const CLOSED_KEY = "shelley_closed_page_tabs";
const CLOSED_CONVERSATIONS = 100;

function closedTabs(): [string, string[]][] {
  return JSON.parse(localStorage.getItem(CLOSED_KEY) ?? "[]") as [string, string[]][];
}

function loadClosed(id: string | null): Set<string> {
  return new Set(closedTabs().find(([conversation]) => conversation === id)?.[1]);
}

function saveClosed(id: string, urls: Set<string>) {
  const kept = closedTabs().filter(([conversation]) => conversation !== id);
  kept.push([id, [...urls]]);
  localStorage.setItem(CLOSED_KEY, JSON.stringify(kept.slice(-CLOSED_CONVERSATIONS)));
}

/** The page frame the user is in, by its tab key. */
function focusedFrame(): string | undefined {
  const el = document.activeElement;
  return el instanceof HTMLIFrameElement ? el.dataset.pageTab : undefined;
}

/** A task's page tabs (workspace mode): its VM's site, the pages its agent
 *  declared; the agent's later links move them. active is the shown tab's key, or null for the chat; a tab's
 *  frame stays mounted once shown, and follows its tab to a new page. The
 *  shown frames reload when the agent's turn ends and when it links their
 *  page again, but never under a user who is in one: that one reloads once
 *  focus leaves it. */
export function usePageTabs(
  conversationId: Ref<string | null>,
  messages: Ref<Message[]>,
  working: Ref<boolean>,
) {
  const workspace = useWorkspace();
  const { t } = useI18n();
  const closed = ref(new Set<string>());
  const active = ref<string | null>(null);
  const mounted = ref(new Set<string>());
  // By URL.
  const frames = reactive(new Map<string, FrameState>());
  // By tab key.
  const reloads = reactive(new Map<string, number>());
  // How many messages there were when each frame last loaded.
  const loadedWith = new Map<string, number>();
  const stale = new Set<string>();
  const linked = new Map<string, Pick<PageTab, "url" | "linked">>();

  const vm = computed(() => workspace.vmFor(conversationId.value));

  const tabs = computed<PageTabView[]>(() => {
    if (!conversationId.value || workspace.isAssistant(conversationId.value)) return [];
    const current = vm.value;
    // Without a VM, localhost is this host's, which the exe.dev proxy serves.
    const options = current ? null : localhostLinkOptionsFromInit();
    const links: PageLink[] = [];
    if (current?.status === "ready")
      links.push({
        url: current.https_url || `https://${current.vm_name}.exe.xyz/`,
        title: current.vm_name,
        declared: true,
      });
    for (const link of pageLinks(messages.value))
      links.push({ ...link, url: options ? rewriteLocalhostLink(link.url, options) : link.url });
    return pageTabs(links, closed.value).map((tab) => {
      const state = frames.get(tab.url) ?? { frame: null, error: "" };
      return {
        ...tab,
        ...state,
        blocked: state.frame?.blocked ? t(BLOCKED[state.frame.blocked]) : "",
        reloadedAt: reloads.get(tab.key) ?? 0,
      };
    });
  });

  async function probe(url: string, refresh: boolean) {
    frames.set(url, { frame: refresh ? null : (frames.get(url)?.frame ?? null), error: "" });
    try {
      const failure = t("pageTabCheckFailed").replace("{url}", url);
      frames.set(url, { frame: await pageFrame(url, failure, refresh), error: "" });
    } catch (err) {
      frames.set(url, { frame: null, error: errorMessage(err) });
    }
  }

  function reload(key: string) {
    reloads.set(key, Date.now());
    loadedWith.set(key, messages.value.length);
  }
  /** Reloads the stale frames the user isn't in. */
  function flush() {
    const focused = focusedFrame();
    for (const key of stale)
      if (key !== focused) {
        stale.delete(key);
        reload(key);
      }
  }
  // A turn's end and its last message, which may link the page again,
  // arrive apart: one reload serves both.
  let settle: ReturnType<typeof setTimeout> | null = null;
  function refresh(keys: Iterable<string>) {
    for (const key of keys) if (mounted.value.has(key)) stale.add(key);
    if (settle) clearTimeout(settle);
    settle = setTimeout(flush, 250);
  }

  watch(
    conversationId,
    (id) => {
      closed.value = loadClosed(id);
      active.value = null;
      mounted.value = new Set();
      if (settle) clearTimeout(settle);
      stale.clear();
      linked.clear();
      loadedWith.clear();
    },
    { immediate: true },
  );
  watch(tabs, (current) => {
    for (const tab of current) if (!frames.has(tab.url)) void probe(tab.url, false);
    if (active.value && !current.some((tab) => tab.key === active.value)) active.value = null;
    const relinked: string[] = [];
    for (const tab of current) {
      const before = linked.get(tab.key);
      if (before?.url === tab.url && tab.linked > before.linked) relinked.push(tab.key);
      // A frame moving to another page loads it anew.
      else if (before && before.url !== tab.url) loadedWith.set(tab.key, messages.value.length);
      linked.set(tab.key, { url: tab.url, linked: tab.linked });
    }
    refresh(relinked);
  });
  watch(working, (now, before) => {
    // The frames that loaded before the agent's latest work.
    if (before && !now)
      refresh(
        [...mounted.value].filter((key) => (loadedWith.get(key) ?? 0) < messages.value.length),
      );
  });
  // Focus leaving a frame lands in Shelley's page, or on its window.
  document.addEventListener("focusin", flush);
  window.addEventListener("focus", flush);

  onUnmounted(() => {
    if (settle) clearTimeout(settle);
    document.removeEventListener("focusin", flush);
    window.removeEventListener("focus", flush);
  });

  return {
    tabs,
    active,
    mounted,
    /** Shows the tab's page, or the chat for null. */
    select(key: string | null) {
      active.value = key;
      if (key && !mounted.value.has(key)) {
        mounted.value = new Set(mounted.value).add(key);
        loadedWith.set(key, messages.value.length);
      }
    },
    /** Probes the tab's page again and replaces its frame. */
    reload(tab: PageTab) {
      void probe(tab.url, true).then(() => reload(tab.key));
    },
    /** Hides the tab in this conversation until a link moves it. */
    close(tab: PageTab) {
      const id = conversationId.value;
      if (!id) return;
      closed.value = new Set(closed.value).add(tab.url);
      saveClosed(id, closed.value);
      const kept = new Set(mounted.value);
      kept.delete(tab.key);
      mounted.value = kept;
      if (active.value === tab.key) active.value = null;
    },
  };
}

export type PageTabs = ReturnType<typeof usePageTabs>;
