// Durable browser copy of review recordings. Audio chunks and event batches
// are written as they are produced and kept until the server has accepted the
// finished recording, so a crash, reload, or failed upload never loses one.
import { openDB, type DBSchema, type IDBPDatabase } from "idb";
import type { ChatRequest } from "../../types";
import type { ReviewEvent } from "./reviewCapture";

// How a conversation started by a recording is set up; taken at start.
export interface NewReviewConversation {
  cwd?: string;
  model: string;
  conversation_options?: ChatRequest["conversation_options"];
}

export interface ReviewSession {
  id: string;
  cwd: string;
  mimeType: string;
  // Wall-clock time of audio t=0.
  startedAt: string;
  durationMs: number;
  // Receives the recording; bound when recording starts. One made outside a
  // conversation (newConversation) goes to the page's draft when it has one,
  // else to a draft of its own made when first sent.
  conversationId?: string;
  newConversation?: NewReviewConversation;
  // conversationId is a draft this recording made; discarding deletes it
  // while it is still empty.
  ownsConversation?: boolean;
  audioPath?: string;
  eventsPath?: string;
}

interface Schema extends DBSchema {
  sessions: { key: string; value: ReviewSession };
  chunks: { key: [string, number]; value: { session: string; seq: number; data: ArrayBuffer } };
  events: { key: [string, number]; value: { session: string; seq: number; events: ReviewEvent[] } };
}

const DB_NAME = "shelley-review-recordings";

export class ReviewRecordingStore {
  private db: Promise<IDBPDatabase<Schema>>;

  constructor(name = DB_NAME) {
    this.db = openDB<Schema>(name, 1, {
      upgrade(db) {
        db.createObjectStore("sessions", { keyPath: "id" });
        db.createObjectStore("chunks", { keyPath: ["session", "seq"] });
        db.createObjectStore("events", { keyPath: ["session", "seq"] });
      },
    });
  }

  async create(session: ReviewSession): Promise<void> {
    await (await this.db).put("sessions", session);
  }

  async get(id: string): Promise<ReviewSession | undefined> {
    return (await this.db).get("sessions", id);
  }

  async list(): Promise<ReviewSession[]> {
    return (await this.db).getAll("sessions");
  }

  async update(id: string, patch: Partial<ReviewSession>): Promise<ReviewSession> {
    const tx = (await this.db).transaction("sessions", "readwrite");
    const current = await tx.store.get(id);
    if (!current) throw new Error(`Recording ${id} is no longer saved in this browser`);
    const next = { ...current, ...patch };
    await tx.store.put(next);
    await tx.done;
    return next;
  }

  // Plain bytes, not Blobs: WebKit can stall IndexedDB transactions holding Blobs.
  async appendChunk(id: string, seq: number, data: ArrayBuffer, durationMs: number): Promise<void> {
    const tx = (await this.db).transaction(["chunks", "sessions"], "readwrite");
    const session = await tx.objectStore("sessions").get(id);
    // A tab resumed after its recording was sent or discarded must not leave
    // chunks nobody can find.
    if (!session) throw new Error(`Recording ${id} is no longer saved in this browser`);
    await tx.objectStore("chunks").put({ session: id, seq, data });
    await tx.objectStore("sessions").put({ ...session, durationMs });
    await tx.done;
  }

  async appendEvents(id: string, seq: number, events: ReviewEvent[]): Promise<void> {
    await (await this.db).put("events", { session: id, seq, events });
  }

  async audio(id: string, mimeType: string): Promise<Blob> {
    const chunks = await (await this.db).getAll("chunks", sessionRange(id));
    return new Blob(
      chunks.map((chunk) => chunk.data),
      { type: mimeType },
    );
  }

  async events(id: string): Promise<ReviewEvent[]> {
    const batches = await (await this.db).getAll("events", sessionRange(id));
    return batches.flatMap((batch) => batch.events);
  }

  async delete(id: string): Promise<void> {
    const tx = (await this.db).transaction(["sessions", "chunks", "events"], "readwrite");
    await Promise.all([
      tx.objectStore("chunks").delete(sessionRange(id)),
      tx.objectStore("events").delete(sessionRange(id)),
      tx.objectStore("sessions").delete(id),
    ]);
    await tx.done;
  }
}

function sessionRange(id: string): IDBKeyRange {
  return IDBKeyRange.bound([id, 0], [id, Number.MAX_SAFE_INTEGER]);
}
