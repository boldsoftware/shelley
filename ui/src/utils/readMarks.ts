import type { ConversationRead, ConversationUnreadMark } from "../types";
import type { ConversationReads } from "../generated-types";

/**
 * The user's private read state (see "Unread" in API.md): their first visit,
 * how far they have read each conversation, and their "Mark as unread" marks.
 * The server only moves these forward, so they merge with MAX.
 */
export class ReadMarks {
  firstSeenAt: number | null = null;
  readonly reads = new Map<string, number>();
  readonly marks = new Map<string, number>();
  /** The open conversation marked unread while open. It stays unread, with
   * nothing acknowledged, until it is next opened or a receipt reaches the
   * mark. */
  held: string | null = null;
  /** Conversations this client is marking unread: nothing is acknowledged
   * for them until the mark arrives. */
  readonly marking = new Set<string>();
  /** False from a reconnect until its snapshot: the list replay comes first,
   * and the snapshot may bring a mark to hold. */
  synced = true;

  /** The stream's snapshot, with the conversation shown. Marks in the first
   * one are read by opening, except this client's on their way; ones a
   * reconnect brings were set while open. */
  snapshot(reads: ConversationReads, open: string | null) {
    const reconnect = this.firstSeenAt !== null;
    this.firstSeenAt = reads.first_seen_at;
    this.synced = true;
    reads.receipts?.forEach((receipt) => this.read(receipt));
    reads.unread_marks?.forEach((mark) =>
      this.mark(mark, reconnect || this.marking.has(mark.conversation_id) ? open : null),
    );
  }

  read({ conversation_id: id, read_at }: ConversationRead) {
    if (read_at > (this.reads.get(id) ?? 0)) this.reads.set(id, read_at);
  }

  /** A mark, with the conversation shown, which a new mark holds. */
  mark({ conversation_id: id, unread_at }: ConversationUnreadMark, open: string | null) {
    if (unread_at <= (this.marks.get(id) ?? 0)) return;
    this.marks.set(id, unread_at);
    if (id === open && unread_at > (this.reads.get(id) ?? 0)) this.held = id;
  }

  /** The stream reconnected; acknowledge nothing until its snapshot. */
  reconnected() {
    this.synced = false;
  }

  /** The hold is over: the user showed another conversation (or none), or
   * wrote in the held one. */
  release() {
    this.held = null;
  }

  /** The time a receipt must reach to read c: its latest reply or mark, or
   * 0 when c is read. */
  unreadTime(c: { conversation_id: string; last_attention_at?: number }): number {
    if (this.firstSeenAt === null) return 0;
    const id = c.conversation_id;
    const time = Math.max(c.last_attention_at ?? 0, this.marks.get(id) ?? 0);
    return time > Math.max(this.firstSeenAt, this.reads.get(id) ?? 0) ? time : 0;
  }

  /** What showing c acknowledges: its unreadTime, unless c's mark is held or
   * on its way, or a reconnect's snapshot is still to come. */
  ackTime(c: { conversation_id: string; last_attention_at?: number }): number {
    const id = c.conversation_id;
    const held = id === this.held && this.unreadMark(id);
    return !this.synced || held || this.marking.has(id) ? 0 : this.unreadTime(c);
  }

  /** The conversation's mark no receipt has reached, or 0. */
  unreadMark(id: string): number {
    const at = this.marks.get(id) ?? 0;
    return at > (this.reads.get(id) ?? 0) ? at : 0;
  }
}
