// globalStream.ts — single long-lived EventSource for the whole UI.
//
// Subscribes to /api/stream2. The server delivers per-conversation events for
// ALL active conversations on a single connection, plus server-wide events
// (conversation_list_patch, notification_event, heartbeat). Per-conversation
// events are tagged with `conversation_id` for routing.
//
// This module fans out:
//   * persistent updates (messages, conversation, context_window_size,
//     conversation_state) → messageStore
//   * transient updates (tool_progress, stream_delta, agent_working) →
//     messageStore transient state
//   * list patches → onListPatch handler
//   * notification events → onNotificationEvent handler
//
// Components NEVER open their own EventSource; they subscribe to messageStore.

import type {
  ConversationListPatchEvent,
  NotificationEvent,
  StreamResponse,
  Message,
} from "../types";
import { api } from "./api";
import { messageStore } from "./messageStore";

export type StreamStatus = "connected" | "reconnecting" | "disconnected";

// Server sends a heartbeat every 30s. If we go this long without any frame
// the connection is presumed dead and we force a reconnect.
const HEARTBEAT_TIMEOUT_MS = 60000;
// On a foreground/network resume, treat the connection as stale (and
// reconnect) if we haven't seen a frame within this window — one missed
// 30s heartbeat plus margin. Shorter than HEARTBEAT_TIMEOUT_MS because here
// we have a positive signal (user returned) and want to recover fast.
const STALE_MS = 35000;
// A connection must survive this long before a subsequent error resets the
// reconnect backoff. Without this, a server that accepts the stream and
// drops it moments later (e.g. subscriber-queue overflow under a stream
// flood, a proxy shedding load) traps the client in a hot 1s loop: every
// reopen zeroed the attempt counter, and every reopen replays the full
// conversation-list snapshot and triggers a REST backfill — the client
// hammering the server with its most expensive requests exactly while the
// server is struggling. With the window, connect→quick-drop cycles keep
// escalating backoff (1s→2s→5s→30s) like consecutive failures.
const STABLE_CONNECTION_MS = 30000;

export interface GlobalStreamOptions {
  getHash: () => string | null;
  onListPatch: (event: ConversationListPatchEvent) => void;
  onNotificationEvent?: (event: NotificationEvent) => void;
  onStatusChange?: (status: StreamStatus) => void;
  /**
   * Called once after every successful re-establishment of the EventSource
   * (i.e. after at least one disconnect-then-connect transition; not on
   * the very first connect). Used by App to refresh the focused
   * conversation's history via REST, since any conversation may have
   * received new messages while we were disconnected.
   */
  onReconnect?: () => void;
}

export interface GlobalStreamHandle {
  close: () => void;
  forceReconnect: () => void;
}

function extractToolUseIds(msg: Message): string[] {
  if (msg.type !== "tool" && msg.type !== "user") return [];
  try {
    const raw = msg.llm_data;
    const llmData = raw
      ? typeof raw === "string"
        ? (JSON.parse(raw) as { Content?: Array<{ Type: number; ToolUseID?: string }> })
        : (raw as { Content?: Array<{ Type: number; ToolUseID?: string }> })
      : null;
    if (!llmData?.Content) return [];
    return llmData.Content.filter((c) => c.Type === 6 && c.ToolUseID)
      .map((c) => c.ToolUseID!)
      .filter(Boolean);
  } catch {
    return [];
  }
}

export function connectGlobalStream({
  getHash,
  onListPatch,
  onNotificationEvent,
  onStatusChange,
  onReconnect,
}: GlobalStreamOptions): GlobalStreamHandle {
  let closed = false;
  let eventSource: EventSource | null = null;
  let reconnectTimer: number | null = null;
  let heartbeatTimer: number | null = null;
  let attempts = 0;
  let lastStatus: StreamStatus | null = null;
  // Wall-clock timestamp at which the CURRENT EventSource delivered its
  // first frame (0 while none has). Used to distinguish a connection that
  // died after honest service from one the server dropped moments after
  // accepting: only the former earns a backoff reset. See
  // STABLE_CONNECTION_MS.
  let connectionOpenedAt = 0;
  // Wall-clock timestamp of the last frame (open or any message, incl.
  // heartbeat) received on the current connection. This is the source of
  // truth for connection liveness: unlike setTimeout-based watchdogs, it is
  // immune to background-tab timer throttling/freezing, so when the tab
  // returns to the foreground we can tell — by comparing against Date.now()
  // — whether the socket has gone silent (a zombie connection the browser
  // never surfaced as an error) and reconnect immediately.
  let lastFrameAt = Date.now();
  // True once we have successfully connected at least once. Used to
  // distinguish a true reconnect from the initial connect: only the former
  // triggers markAllStale() + onReconnect().
  let hasEverConnected = false;
  // True while the EventSource is in the middle of being re-established
  // after a disconnect. Set on error, cleared on the next successful open.
  let isReconnecting = false;

  const setStatus = (s: StreamStatus) => {
    if (s === lastStatus) return;
    lastStatus = s;
    onStatusChange?.(s);
  };

  const clearReconnect = () => {
    if (reconnectTimer !== null) {
      window.clearTimeout(reconnectTimer);
      reconnectTimer = null;
    }
  };

  const clearHeartbeat = () => {
    if (heartbeatTimer !== null) {
      window.clearTimeout(heartbeatTimer);
      heartbeatTimer = null;
    }
  };

  const resetHeartbeat = () => {
    clearHeartbeat();
    heartbeatTimer = window.setTimeout(() => {
      console.warn("globalStream: no heartbeat in 60s, forcing reconnect");
      reconnectNow();
    }, HEARTBEAT_TIMEOUT_MS);
  };

  // reconnectNow tears down the current EventSource and reconnects right away,
  // bypassing the error-backoff timer. Used by the user-resume paths
  // (visibilitychange, pageshow, online) and the heartbeat watchdog. It flags
  // a reconnect-in-progress so the next successful open triggers
  // markAllStale() + onReconnect(), backfilling history that may have changed
  // while we were silent.
  const reconnectNow = () => {
    if (closed) return;
    if (hasEverConnected) isReconnecting = true;
    attempts = 0;
    eventSource?.close();
    eventSource = null;
    clearHeartbeat();
    clearReconnect();
    connect();
  };

  // reconnectIfStale reconnects only when the current connection looks dead:
  // no EventSource, an explicitly-closed one, or one that hasn't delivered a
  // frame within STALE_MS (one missed heartbeat plus margin). Called when the
  // tab is brought back to the foreground or the network returns, where a
  // zombie socket can report readyState OPEN while being silently dead.
  const reconnectIfStale = () => {
    if (closed) return;
    // A CONNECTING socket (readyState 0) is mid-handshake and making
    // progress; tearing it down would only restart it. Leave it be and let
    // its onopen/onerror resolve. We only act on a missing socket, an
    // explicitly-closed one, or an OPEN-but-silent (zombie) one.
    if (eventSource && eventSource.readyState === 0) return;
    const stale =
      !eventSource || eventSource.readyState === 2 || Date.now() - lastFrameAt > STALE_MS;
    if (stale) reconnectNow();
  };

  const handleEvent = (data: StreamResponse) => {
    if (data.conversation_list_patch) {
      onListPatch(data.conversation_list_patch);
    }
    if (data.notification_event && onNotificationEvent) {
      onNotificationEvent(data.notification_event);
    }

    const convId = data.conversation_id;
    if (!convId) return;

    // Persistent state
    if (data.messages && data.messages.length > 0) {
      // Clear streaming text / tool progress for tools that just produced results.
      const toolIds: string[] = [];
      let sawAgentMsg = false;
      let maxSeq = 0;
      for (const m of data.messages) {
        toolIds.push(...extractToolUseIds(m));
        if (m.type === "agent") sawAgentMsg = true;
        if (m.sequence_id > maxSeq) maxSeq = m.sequence_id;
      }
      if (toolIds.length > 0) messageStore.clearToolProgress(convId, toolIds);
      if (sawAgentMsg) messageStore.resetStreaming(convId);
      messageStore.upsertMessages(convId, data.messages);
      if (maxSeq > 0) messageStore.setMaxSequenceIdKnown(convId, maxSeq);
    }
    if (data.conversation) {
      // NB: we deliberately do NOT mirror data.conversation.agent_working
      // into the transient store here. Per-conversation Conversation rows
      // arrive embedded in unrelated stream events (new-message broadcast,
      // git-state change, cwd change, etc.) and can carry a stale snapshot
      // taken before an in-flight SetConversationAgentWorking commit.
      // Authoritative agent_working sync happens via (a) conversation_state
      // events fired synchronously from SetAgentWorking, and (b) the
      // conversation_list_patch stream, whose updates are driven by the DB
      // commit hook and therefore strictly trail the matching write —
      // handled in App.handleConversationListPatch.
      messageStore.setConversation(convId, data.conversation);
    }
    if (typeof data.context_window_size === "number") {
      messageStore.setContextWindowSize(convId, data.context_window_size);
    }
    // NB: we deliberately do NOT mirror data.conversation_state.working
    // into messageStore here. The conversation_list_patch stream is the
    // single authoritative source of truth for agent_working:
    // server-side recomputeMu serializes patch emission so list patches
    // arrive in a strict old_hash→new_hash chain, while per-conversation
    // conversation_state events ride a separate streamPub fan-out and
    // can race with the list patches at the client. If we let both
    // update agentWorking, a stale state event from an earlier transition
    // can stomp a fresher list-patch value — the "thinking pill
    // stays on / flickers" symptom (iOS hit the mirror image of this
    // race and fixed it in a4ce86d1f the same way). List patches now
    // pump the authoritative value via App.handleConversationListPatch.
    //
    // We still leave conversation_state in the protocol because the
    // server's per-conversation /api/conversation/<id>/stream and legacy
    // iOS clients consume it; this client just no longer trusts it for
    // working state.

    // Transient state
    if (data.tool_progress) {
      messageStore.setToolProgress(convId, data.tool_progress);
    }
    if (data.stream_delta?.type === "text") {
      messageStore.appendStreamText(convId, data.stream_delta.text);
    }
    if (data.stream_delta?.type === "thinking") {
      messageStore.appendStreamThinking(convId, data.stream_delta.text);
    }
  };

  const connect = () => {
    if (closed) return;
    clearReconnect();
    eventSource?.close();
    // Treat the start of a connection attempt as a liveness checkpoint so a
    // resume signal (visibilitychange/pageshow/online) that races the new
    // socket's onopen doesn't see the previous connection's stale
    // lastFrameAt and tear down the freshly-opened one.
    lastFrameAt = Date.now();
    connectionOpenedAt = 0;
    eventSource = api.createStream({ conversationListHash: getHash() ?? undefined });

    const markConnected = () => {
      // NB: attempts is NOT reset here. A connection only counts as healthy
      // once it has survived STABLE_CONNECTION_MS (judged at error time);
      // resetting on open let a server stuck in an accept-then-drop cycle
      // pin the client at the minimum 1s delay forever.
      if (connectionOpenedAt === 0) connectionOpenedAt = Date.now();
      lastFrameAt = Date.now();
      setStatus("connected");
      if (isReconnecting) {
        // We just re-established after a disconnect. Any conversation could
        // have received new messages while we were down; flag every cached
        // record as needing a fresh REST backfill the next time it's focused,
        // and let the host refresh the currently-focused conversation now.
        isReconnecting = false;
        messageStore.markAllStale();
        onReconnect?.();
      }
      hasEverConnected = true;
    };

    eventSource.onopen = () => {
      markConnected();
      resetHeartbeat();
    };

    eventSource.onmessage = (ev) => {
      markConnected();
      resetHeartbeat();
      try {
        const data = JSON.parse(ev.data) as StreamResponse;
        handleEvent(data);
      } catch (err) {
        console.error("globalStream: failed to parse event:", err);
      }
    };

    eventSource.onerror = () => {
      if (closed) return;
      eventSource?.close();
      eventSource = null;
      clearHeartbeat();
      // A connection that survived STABLE_CONNECTION_MS before dying was
      // genuinely healthy: start the backoff ladder fresh. One that died
      // sooner (or never delivered a frame) is treated as a consecutive
      // failure so accept-then-drop cycles keep escalating 1s→2s→5s→30s.
      if (connectionOpenedAt !== 0 && Date.now() - connectionOpenedAt >= STABLE_CONNECTION_MS) {
        attempts = 0;
      }
      connectionOpenedAt = 0;
      attempts += 1;
      // Only mark stale on the first reconnect attempt after a confirmed
      // disconnect, not on every retry.
      if (hasEverConnected) isReconnecting = true;
      setStatus(attempts > 3 ? "disconnected" : "reconnecting");
      const delay = attempts <= 1 ? 1000 : attempts === 2 ? 2000 : attempts === 3 ? 5000 : 30000;
      reconnectTimer = window.setTimeout(connect, delay);
    };
  };

  // On iOS Safari and other mobile browsers, EventSource may stay nominally
  // open while the underlying TCP connection has been killed by the OS during
  // background. The 60s heartbeat watchdog can't catch this: setTimeout is
  // throttled (and the page eventually frozen) in a hidden tab, so its
  // countdown stalls precisely while we're away and doesn't fire promptly on
  // return. Instead, when the tab is brought back to the foreground — or the
  // page is restored from the bfcache, or the network returns — we compare
  // the wall-clock time since the last received frame and reconnect if the
  // connection has gone silent, regardless of readyState.
  const onVisibility = () => {
    if (document.visibilityState === "visible") reconnectIfStale();
  };
  // pageshow fires on bfcache restores (event.persisted), which on mobile
  // Safari often do NOT fire visibilitychange. A restored page resumes with a
  // long-dead socket, so always check liveness here.
  const onPageShow = () => reconnectIfStale();
  const onOnline = () => reconnectIfStale();
  document.addEventListener("visibilitychange", onVisibility);
  window.addEventListener("pageshow", onPageShow);
  window.addEventListener("online", onOnline);

  connect();

  return {
    close() {
      closed = true;
      clearReconnect();
      clearHeartbeat();
      eventSource?.close();
      eventSource = null;
      document.removeEventListener("visibilitychange", onVisibility);
      window.removeEventListener("pageshow", onPageShow);
      window.removeEventListener("online", onOnline);
    },
    forceReconnect() {
      reconnectNow();
    },
  };
}
