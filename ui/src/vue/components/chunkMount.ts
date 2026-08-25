// Tail-first chunk mounting state, shared between ChatInterface.vue (which
// owns the floor and the background sweep) and ChunkHost.vue (which decides
// per chunk whether to render content or a fixed-height placeholder).
//
// Why provide/inject instead of props: the floor moves on every sweep tick,
// and as a prop it would re-render the whole ChatInterface template (every
// chunk, thousands of nodes) per tick. Injected refs keep each tick's patch
// scoped to the ChunkHosts whose mounted state actually flipped.
import type { InjectionKey, Ref } from "vue";

export interface ChunkMountState {
  // Chunks with globalIndex >= floor render. 0 = everything mounted.
  floor: Ref<number>;
  // Chunks with globalIndex < sweptTop render: the background sweep mounts
  // history top-down (oldest first), because a chunk mounted far above the
  // viewport books at the same estimated height as its placeholder — the
  // swap is height-neutral — whereas a chunk mounted just above the viewport
  // lays out immediately and shifts the content the user is reading.
  sweptTop: Ref<number>;
  // Chunk KEYS revealed out of order (scrolled near the viewport, TOC or
  // fragment jumps) while still between sweptTop and floor. Keys, not
  // indexes: chunk keys are stable when a mid-history restructure
  // (regeneration, compaction) renumbers globalIndex. Reactive Set.
  revealed: Set<string>;
  // A placeholder scrolled near the viewport asks for its chunk (and, via
  // ChatInterface, a small look-behind window) to mount.
  reveal: (globalIndex: number) => void;
  // Mount the chunk containing a message / tool-use / URL fragment before a
  // programmatic jump to it (TOC entries, #m-…/#t-… fragments). Returns true
  // when the target was found in not-fully-mounted history; the caller should
  // re-query the DOM after nextTick. False means "already mounted or unknown"
  // — callers treat both as "just query the DOM".
  revealTarget: (target: { messageId?: string; toolUseId?: string; fragment?: string }) => boolean;
}

export const chunkMountKey: InjectionKey<ChunkMountState> = Symbol("chunkMount");
