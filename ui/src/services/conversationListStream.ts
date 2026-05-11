import type {
  ConversationListPatchEvent,
  ConversationListPatchOp,
  ConversationWithState,
} from "../types";
import { api } from "./api";

function decodePointer(path: string): string[] {
  if (path === "") return [];
  if (!path.startsWith("/")) {
    throw new Error(`invalid JSON pointer: ${path}`);
  }
  return path
    .slice(1)
    .split("/")
    .map((part) => part.replace(/~1/g, "/").replace(/~0/g, "~"));
}

function cloneValue<T>(value: T): T {
  return value === undefined ? value : JSON.parse(JSON.stringify(value));
}

function getAt(doc: unknown, path: string): unknown {
  let cur = doc;
  for (const part of decodePointer(path)) {
    if (Array.isArray(cur)) {
      const idx = Number(part);
      if (!Number.isInteger(idx) || idx < 0 || idx >= cur.length) {
        throw new Error(`bad array index in patch path: ${path}`);
      }
      cur = cur[idx];
    } else if (cur !== null && typeof cur === "object") {
      cur = (cur as Record<string, unknown>)[part];
    } else {
      throw new Error(`cannot traverse patch path: ${path}`);
    }
  }
  return cur;
}

function encodePointer(parts: string[]): string {
  if (parts.length === 0) return "";
  return `/${parts.map((part) => part.replace(/~/g, "~0").replace(/\//g, "~1")).join("/")}`;
}

function parentAndKey(doc: unknown, path: string): { parent: unknown; key: string } {
  const parts = decodePointer(path);
  if (parts.length === 0) {
    throw new Error("root path has no parent");
  }
  return {
    parent: parts.length === 1 ? doc : getAt(doc, encodePointer(parts.slice(0, -1))),
    key: parts[parts.length - 1],
  };
}

function setAt(doc: unknown, path: string, value: unknown, mustExist: boolean): unknown {
  if (path === "") return cloneValue(value);
  const { parent, key } = parentAndKey(doc, path);
  const nextValue = cloneValue(value);
  if (Array.isArray(parent)) {
    const idx = Number(key);
    if (!Number.isInteger(idx) || idx < 0 || idx > parent.length) {
      throw new Error(`bad array index in patch path: ${path}`);
    }
    if (mustExist && idx >= parent.length) {
      throw new Error(`array index out of range in patch path: ${path}`);
    }
    if (idx === parent.length) {
      parent.push(nextValue);
    } else if (mustExist) {
      parent[idx] = nextValue;
    } else {
      parent.splice(idx, 0, nextValue);
    }
    return doc;
  }
  if (parent !== null && typeof parent === "object") {
    const obj = parent as Record<string, unknown>;
    if (mustExist && !(key in obj)) {
      throw new Error(`missing object key in patch path: ${path}`);
    }
    obj[key] = nextValue;
    return doc;
  }
  throw new Error(`cannot set patch path: ${path}`);
}

function removeAt(doc: unknown, path: string): unknown {
  if (path === "") throw new Error("cannot remove document root");
  const { parent, key } = parentAndKey(doc, path);
  if (Array.isArray(parent)) {
    const idx = Number(key);
    if (!Number.isInteger(idx) || idx < 0 || idx >= parent.length) {
      throw new Error(`bad array index in patch path: ${path}`);
    }
    parent.splice(idx, 1);
    return doc;
  }
  if (parent !== null && typeof parent === "object") {
    delete (parent as Record<string, unknown>)[key];
    return doc;
  }
  throw new Error(`cannot remove patch path: ${path}`);
}

function validateOp(op: ConversationListPatchOp): void {
  if (typeof op.path !== "string") {
    throw new Error(`patch op ${op.op} is missing path`);
  }
  if ((op.op === "add" || op.op === "replace") && !("value" in op)) {
    throw new Error(`patch op ${op.op} is missing value`);
  }
  if (op.op === "move" && typeof op.from !== "string") {
    throw new Error("move patch is missing from");
  }
}

export function applyConversationListPatch(
  state: ConversationWithState[],
  patch: ConversationListPatchOp[],
): ConversationWithState[] {
  let doc: unknown = cloneValue(state);
  for (const op of patch) {
    validateOp(op);
    switch (op.op) {
      case "replace":
        doc = setAt(doc, op.path, op.value, op.path !== "");
        break;
      case "add":
        doc = setAt(doc, op.path, op.value, false);
        break;
      case "remove":
        doc = removeAt(doc, op.path);
        break;
      case "move": {
        const value = cloneValue(getAt(doc, op.from!));
        doc = removeAt(doc, op.from!);
        doc = setAt(doc, op.path, value, false);
        break;
      }
      default: {
        const exhaustive: never = op.op;
        throw new Error(`unsupported patch op: ${exhaustive}`);
      }
    }
  }
  if (!Array.isArray(doc)) {
    throw new Error("conversation list patch did not produce an array");
  }
  return doc as ConversationWithState[];
}
export interface ConversationListStreamHandle {
  close: () => void;
}

interface ConversationListStreamOptions {
  getHash: () => string | null;
  onPatch: (event: ConversationListPatchEvent) => void;
  onStatusChange?: (status: "connected" | "reconnecting" | "disconnected") => void;
}

export function connectConversationListStream({
  getHash,
  onPatch,
  onStatusChange,
}: ConversationListStreamOptions): ConversationListStreamHandle {
  let closed = false;
  let eventSource: EventSource | null = null;
  let reconnectTimer: number | null = null;
  let attempts = 0;

  const clearReconnect = () => {
    if (reconnectTimer !== null) {
      clearTimeout(reconnectTimer);
      reconnectTimer = null;
    }
  };

  const connect = () => {
    if (closed) return;
    clearReconnect();
    eventSource?.close();
    eventSource = api.createStream({ conversationListHash: getHash() ?? undefined });

    eventSource.onmessage = (event) => {
      attempts = 0;
      onStatusChange?.("connected");
      try {
        const data = JSON.parse(event.data) as {
          conversation_list_patch?: ConversationListPatchEvent;
        };
        if (data.conversation_list_patch) {
          onPatch(data.conversation_list_patch);
        }
      } catch (err) {
        console.error("Failed to apply conversation list patch:", err);
        onStatusChange?.("reconnecting");
        eventSource?.close();
        eventSource = null;
        reconnectTimer = window.setTimeout(connect, 1000);
      }
    };

    eventSource.onopen = () => {
      attempts = 0;
      onStatusChange?.("connected");
    };

    eventSource.onerror = () => {
      if (closed) return;
      eventSource?.close();
      eventSource = null;
      attempts += 1;
      onStatusChange?.(attempts > 3 ? "disconnected" : "reconnecting");
      const delay = attempts <= 1 ? 1000 : attempts === 2 ? 2000 : attempts === 3 ? 5000 : 30000;
      reconnectTimer = window.setTimeout(connect, delay);
    };
  };

  connect();

  return {
    close() {
      closed = true;
      clearReconnect();
      eventSource?.close();
      eventSource = null;
    },
  };
}
