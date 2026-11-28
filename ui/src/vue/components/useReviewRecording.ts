// Narrated review recordings: microphone audio plus a timestamped log of what
// was on screen, pointed at, and selected (reviewCapture.ts). Both are kept in
// IndexedDB as they are produced and only dropped once the conversation has
// accepted the `/transcription` command that hands them to the server.
import { onBeforeUnmount, onMounted, ref, shallowRef } from "vue";
import { api } from "../../services/api";
import { loadCachedDraft } from "../../services/draftCache";
import { SLASH_COMMANDS } from "../../utils/slashCommands";
import {
  startReviewCapture,
  type ReviewCapture,
  type ReviewCaptureState,
  type ReviewContext,
  type ReviewEvent,
} from "./reviewCapture";
import { startRecordingMeter } from "./recordingMeter";
import {
  ReviewRecordingStore,
  type NewReviewConversation,
  type ReviewSession,
} from "./reviewRecordingStore";

export type ReviewRecordingPhase = "idle" | "starting" | "recording" | "stopping" | "sending";

const WAVEFORM_BARS = 10;
const EMPTY_STATE: ReviewCaptureState = {
  pointer: "",
  pointerText: "",
  selection: "",
  screen: "",
  view: "",
};

interface ActiveRecording {
  id: string;
  // The page's draft, once it has one; the recording goes there.
  draft?: string;
  t0: number;
  recorder: MediaRecorder;
  stream: MediaStream;
  capture: ReviewCapture;
  stopMeter: () => void;
  release: Release;
  buffer: ReviewEvent[];
  eventSeq: number;
  chunkSeq: number;
  writes: Promise<void>;
  storageError: Error | null;
  stopped: Promise<void>;
  stopping: boolean;
  timer: number;
}

let sharedStore: ReviewRecordingStore | null = null;
const store = () => (sharedStore ??= new ReviewRecordingStore());

// A session is in use (recording or sending, in any tab) while this tab or
// another holds its Web Lock; the browser releases it if the tab dies.
const lockName = (id: string) => `shelley-review-recording-${id}`;
type Release = () => Promise<void>;
// Resolves once the lock is held (or null if ifAvailable and it is not);
// the returned release resolves once the lock is actually free.
function requestLock(id: string, ifAvailable: boolean): Promise<Release | null> {
  return new Promise((resolve, reject) => {
    let unlock!: () => void;
    const held = new Promise<void>((done) => (unlock = done));
    const released: Promise<unknown> = navigator.locks.request(
      lockName(id),
      { ifAvailable },
      (lock) => {
        if (!lock) return resolve(null);
        resolve(async () => {
          unlock();
          await released;
        });
        return held;
      },
    );
    released.catch(reject);
  });
}
const holdLock = (id: string) => requestLock(id, false) as Promise<Release>;
const tryLock = (id: string) => requestLock(id, true);

// Web Locks and microphone capture both need a secure context.
export function reviewRecordingSupported(): boolean {
  return (
    window.isSecureContext &&
    !!navigator.locks &&
    typeof navigator.mediaDevices?.getUserMedia === "function" &&
    typeof window.MediaRecorder === "function"
  );
}

const audioExtension = (mimeType: string) => (mimeType.includes("mp4") ? ".mp4" : ".webm");
const fileStamp = (iso: string) => iso.replace(/[-:]/g, "").replace("T", "-").slice(0, 15);

async function reviewJSON(session: ReviewSession): Promise<Blob> {
  const body = JSON.stringify({
    version: 1,
    source: "diff-viewer",
    cwd: session.cwd,
    started_at: session.startedAt,
    duration_ms: Math.round(session.durationMs),
    mime_type: session.mimeType,
    events: await store().events(session.id),
  });
  return new Blob([body], { type: "application/json" });
}

function saveFile(name: string, blob: Blob) {
  const url = URL.createObjectURL(blob);
  const link = document.createElement("a");
  link.href = url;
  link.download = name;
  link.click();
  window.setTimeout(() => URL.revokeObjectURL(url), 60_000);
}

// A recording made outside a live conversation goes to the page's draft,
// promoted with the page's settings; once the page has moved on (or after a
// reload), to the draft it had, else to a draft of its own made with the
// settings taken at start.
export interface ReviewConversationStart {
  settings: NewReviewConversation;
  // The page's draft, while the page is the one recording began on.
  draft: () => string | undefined;
  // That draft, created if need be, and the page's settings now; undefined
  // once the page has moved on or if it is archived.
  resolve: (
    cwd?: string,
  ) => Promise<{ conversationId: string; settings: NewReviewConversation } | undefined>;
}

export function useReviewRecording(options: {
  root: () => HTMLElement | null;
  context: ReviewContext;
  cwd: () => string;
  // Where recordings go: the live conversation, else one started by
  // startConversation (which may throw, e.g. without a model).
  conversationId: () => string | undefined;
  startConversation: () => ReviewConversationStart | undefined;
  // Called as a send begins; the result shows a conversation the recording
  // started, unless the user has moved on since.
  followConversation: () => (conversationId: string) => void;
}) {
  const phase = ref<ReviewRecordingPhase>("idle");
  const error = ref("");
  const sent = ref(false);
  const elapsedMs = ref(0);
  const levels = ref<number[]>(Array.from({ length: WAVEFORM_BARS }, () => 0.15));
  const current = ref<ReviewCaptureState>(EMPTY_STATE);
  const pending = shallowRef<ReviewSession[]>([]);
  // Whether the recording in progress starts a conversation.
  const startsConversation = ref(false);
  const starters = new Map<string, ReviewConversationStart>();
  let active: ActiveRecording | null = null;
  let sentTimer: number | null = null;
  let disposed = false;

  function fail(err: unknown) {
    error.value = err instanceof Error ? err.message : String(err);
  }

  async function refreshPending() {
    if (!reviewRecordingSupported()) return;
    const [sessions, locks] = await Promise.all([store().list(), navigator.locks.query()]);
    const inUse = new Set(locks.held?.map((lock) => lock.name));
    pending.value = sessions
      .filter((session) => !inUse.has(lockName(session.id)))
      .sort((a, b) => a.startedAt.localeCompare(b.startedAt));
  }

  function enqueue(recording: ActiveRecording, write: () => Promise<unknown>) {
    recording.writes = recording.writes.then(async () => {
      await write();
    });
    recording.writes.catch((err: unknown) => {
      if (recording.storageError) return;
      recording.storageError = err instanceof Error ? err : new Error(String(err));
      stop(`Couldn't save the recording in this browser: ${recording.storageError.message}`).catch(
        fail,
      );
    });
  }

  function flushEvents(recording: ActiveRecording) {
    if (!recording.buffer.length) return;
    // In place: the capture keeps appending to this same array.
    const batch = recording.buffer.splice(0);
    const seq = recording.eventSeq++;
    enqueue(recording, () => store().appendEvents(recording.id, seq, batch));
  }

  async function start() {
    const root = options.root();
    const conversationId = options.conversationId();
    if (phase.value !== "idle" || !root) return;
    const starter = conversationId ? undefined : options.startConversation();
    if (!conversationId && !starter) return;
    startsConversation.value = !!starter;
    phase.value = "starting";
    error.value = "";
    sent.value = false;
    const id = crypto.randomUUID();
    let release: Release | null = null;
    let stream: MediaStream | null = null;
    let recorder: MediaRecorder | null = null;
    let stopMeter: (() => void) | null = null;
    let capture: ReviewCapture | null = null;
    try {
      release = await holdLock(id);
      stream = await navigator.mediaDevices.getUserMedia({ audio: true });
      if (disposed) throw new DOMException("The diff viewer closed", "AbortError");
      const mimeType = ["audio/webm;codecs=opus", "audio/webm", "audio/mp4"].find((type) =>
        MediaRecorder.isTypeSupported(type),
      );
      const media = mimeType ? new MediaRecorder(stream, { mimeType }) : new MediaRecorder(stream);
      recorder = media;
      stopMeter = startRecordingMeter(stream, WAVEFORM_BARS, (next) => (levels.value = next));
      const stopped = new Promise<void>((resolve) =>
        media.addEventListener("stop", () => resolve(), { once: true }),
      );
      // Audio t=0 comes after the permission prompt, so that wait adds no
      // skew. The capture's opening snapshot is stamped 0: the state at t=0.
      const t0 = performance.now();
      media.start(1000);
      const buffer: ReviewEvent[] = [];
      capture = startReviewCapture({
        root,
        context: options.context,
        t0,
        emit: (event) => buffer.push(event),
        onState: (state) => (current.value = state),
      });
      const recording: ActiveRecording = {
        id,
        draft: starter?.draft(),
        t0,
        recorder: media,
        stream,
        capture,
        stopMeter,
        release,
        buffer,
        eventSeq: 0,
        chunkSeq: 0,
        writes: Promise.resolve(),
        storageError: null,
        stopped,
        stopping: false,
        timer: 0,
      };
      media.ondataavailable = (event) => {
        if (!event.data.size) return;
        const seq = recording.chunkSeq++;
        const durationMs = performance.now() - t0;
        const data = event.data;
        enqueue(recording, async () =>
          store().appendChunk(id, seq, await data.arrayBuffer(), durationMs),
        );
        pinDraft(recording);
      };
      const interrupt = (reason: string) => () => stop(reason).catch(fail);
      media.onerror = interrupt("The microphone recorder failed; what was recorded is saved.");
      for (const track of stream.getAudioTracks()) {
        track.addEventListener(
          "ended",
          interrupt("The microphone disconnected; what was recorded is saved."),
          { once: true },
        );
      }
      active = recording;
      if (starter) starters.set(id, starter);
      enqueue(recording, () =>
        store().create({
          id,
          cwd: options.cwd(),
          ...(starter
            ? { newConversation: starter.settings, conversationId: recording.draft }
            : { conversationId }),
          mimeType: media.mimeType || "audio/webm",
          startedAt: new Date(performance.timeOrigin + t0).toISOString(),
          durationMs: 0,
        }),
      );
      elapsedMs.value = 0;
      let ticks = 0;
      recording.timer = window.setInterval(() => {
        elapsedMs.value = performance.now() - t0;
        if (++ticks % 4 === 0) flushEvents(recording);
      }, 250);
      phase.value = "recording";
    } catch (err) {
      // Nothing was saved yet; release the microphone and everything else.
      if (recorder && recorder.state !== "inactive") {
        recorder.ondataavailable = null;
        recorder.stop();
      }
      stopMeter?.();
      stream?.getTracks().forEach((track) => track.stop());
      capture?.stop();
      await release?.();
      current.value = EMPTY_STATE;
      phase.value = "idle";
      if (!disposed) fail(err);
    }
  }

  // Ends capture and waits until everything recorded is saved; null if no
  // recording is active or it is already ending.
  async function finish() {
    const recording = active;
    if (!recording || recording.stopping) return null;
    recording.stopping = true;
    phase.value = "stopping";
    const durationMs = performance.now() - recording.t0;
    window.clearInterval(recording.timer);
    // Whatever capture does, the microphone must stop and the audio be saved.
    let captureError: unknown = null;
    try {
      recording.capture.stop();
    } catch (err) {
      captureError = err;
    }
    flushEvents(recording);
    if (recording.recorder.state !== "inactive") recording.recorder.stop();
    await recording.stopped;
    recording.stream.getTracks().forEach((track) => track.stop());
    recording.stopMeter();
    levels.value = levels.value.map(() => 0.15);
    pinDraft(recording);
    enqueue(recording, () => store().update(recording.id, { durationMs }));
    await recording.writes.catch(() => {});
    active = null;
    current.value = EMPTY_STATE;
    return { recording, problem: recording.storageError ?? captureError };
  }

  // Ends capture. Without an interruption reason the recording is sent;
  // otherwise it stays saved and listed as unsent.
  async function stop(interruption?: string) {
    const finished = await finish();
    if (!finished) return;
    const { recording } = finished;
    const problem = interruption ?? finished.problem;
    if (problem) {
      await recording.release();
      phase.value = "idle";
      fail(problem);
      await refreshPending();
      return;
    }
    await send(recording.id, recording.release);
  }

  // Ends capture and throws the recording away.
  async function cancel() {
    const finished = await finish();
    if (!finished) return;
    try {
      await store().delete(finished.recording.id);
      starters.delete(finished.recording.id);
      error.value = "";
    } finally {
      await finished.recording.release();
      phase.value = "idle";
      await refreshPending();
    }
  }

  // Uploads the audio and its events (each at most once), then hands the
  // recording to its conversation. `release` is the session's lock when the
  // caller already holds it.
  async function send(id: string, release: Release | null = null) {
    phase.value = "sending";
    error.value = "";
    const follow = options.followConversation();
    try {
      release ??= await tryLock(id);
      if (!release) throw new Error("This recording is being sent from another tab");
      let session = await store().get(id);
      if (!session) throw new Error("This recording is no longer saved in this browser");
      if (!session.audioPath) {
        const audio = await store().audio(id, session.mimeType);
        if (audio.size === 0) throw new Error("Nothing was recorded");
        const name = `review-${fileStamp(session.startedAt)}${audioExtension(session.mimeType)}`;
        session = await store().update(id, { audioPath: await api.uploadRaw(name, audio) });
      }
      const audioPath = session.audioPath!;
      if (!session.eventsPath) {
        const expected = `${audioPath}.review.json`;
        const name = expected.slice(expected.lastIndexOf("/") + 1);
        const path = await api.uploadRaw(name, await reviewJSON(session));
        if (path !== expected) {
          // An earlier attempt uploaded the events but lost the response, so
          // the name was taken. Start over with a fresh pair of files.
          await store().update(id, { audioPath: undefined, eventsPath: undefined });
          throw new Error("The upload was interrupted; send it again");
        }
        session = await store().update(id, { eventsPath: path });
      }
      for (let replaced = false; ; replaced = true) {
        const created = session.newConversation;
        if (created) {
          const bound = session.conversationId;
          const showing = (starter?: ReviewConversationStart) =>
            starter && (!bound || starter.draft() === bound) ? starter : undefined;
          // On the page it began on, or (after a reload or a return) on its
          // draft's page: that draft, with the page's settings now.
          const starter = showing(starters.get(id)) ?? (bound ? showing(pageStarter()) : undefined);
          const page = starter ? await starter.resolve(created.cwd) : undefined;
          if (page) {
            session = await store().update(id, {
              conversationId: page.conversationId,
              newConversation: page.settings,
            });
          } else if (!bound) {
            const draft = await api.createDraft({ draft: "", ...created });
            // Kept, so a retry goes to the same conversation.
            session = await store().update(id, {
              conversationId: draft.conversation_id,
              ownsConversation: true,
            });
          }
        }
        const conversationId = session.conversationId;
        if (!conversationId) throw new Error("This recording has no conversation to go to");
        if (session.newConversation) {
          const destination = await api.getConversationBySlug(conversationId);
          if (!destination || destination.archived) {
            // Deleted or archived since it was chosen: start another, once.
            starters.delete(id);
            session = await store().update(id, {
              conversationId: undefined,
              ownsConversation: undefined,
            });
            if (replaced) throw new Error("This recording's conversation is gone");
            continue;
          }
        }
        await api.sendMessage(conversationId, {
          message: `${SLASH_COMMANDS.TRANSCRIPTION.command} ${audioPath}`,
          // Not the composer's text: it stays, even through a draft's promotion.
          draft_updated_at: "",
          // These promote a draft; a conversation ignores them.
          ...session.newConversation,
        });
        break;
      }
      const conversationId = session.conversationId!;
      await store().delete(id);
      starters.delete(id);
      if (session.newConversation) follow(conversationId);
      sent.value = true;
      if (sentTimer !== null) window.clearTimeout(sentTimer);
      sentTimer = window.setTimeout(() => (sent.value = false), 5000);
    } catch (err) {
      fail(err);
    } finally {
      await release?.();
      phase.value = "idle";
      await refreshPending();
    }
  }

  // A draft the recording made and that nobody has written in since, here
  // or (as far as the server knows) elsewhere.
  async function dropEmptyDraft(conversationId: string) {
    if (loadCachedDraft(conversationId)?.value.trim()) return;
    const draft = await api.getConversationBySlug(conversationId);
    if (draft?.is_draft && !draft.draft.trim()) await api.deleteConversation(conversationId);
  }

  // The page's draft takes the recording as soon as there is one (say, made
  // for a diff comment), so it goes there even after a reload mid-recording.
  function pinDraft(recording: ActiveRecording) {
    const draft = starters.get(recording.id)?.draft();
    if (!draft || draft === recording.draft) return;
    recording.draft = draft;
    enqueue(recording, () => store().update(recording.id, { conversationId: draft }));
  }

  function pageStarter() {
    try {
      return options.startConversation();
    } catch {
      return undefined;
    }
  }

  async function retry(session: ReviewSession) {
    if (phase.value === "idle") await send(session.id);
  }

  async function discard(session: ReviewSession) {
    const release = await tryLock(session.id);
    if (!release) throw new Error("This recording is being sent from another tab");
    try {
      const saved = await store().get(session.id);
      await store().delete(session.id);
      starters.delete(session.id);
      error.value = "";
      if (saved?.ownsConversation && saved.conversationId) {
        await dropEmptyDraft(saved.conversationId).catch((err) =>
          console.error("Failed to delete the recording's draft:", err),
        );
      }
    } finally {
      await release();
    }
    await refreshPending();
  }

  async function download(session: ReviewSession) {
    const name = `review-${fileStamp(session.startedAt)}${audioExtension(session.mimeType)}`;
    const [audio, events] = await Promise.all([
      store().audio(session.id, session.mimeType),
      reviewJSON(session),
    ]);
    // Back to back, so the browser treats them as one multi-file download.
    saveFile(name, audio);
    saveFile(`${name}.review.json`, events);
  }

  function note(event: Omit<ReviewEvent, "ms">) {
    active?.capture.note(event);
  }

  const onBeforeUnload = (event: BeforeUnloadEvent) => {
    if (phase.value === "idle") return;
    event.preventDefault();
    event.returnValue = "";
  };

  // Keep what was recorded; it is offered again next time.
  const abandon = () =>
    stop("Recording ended when the diff viewer closed; what was recorded is saved.");

  onMounted(() => window.addEventListener("beforeunload", onBeforeUnload));
  onBeforeUnmount(() => {
    disposed = true;
    window.removeEventListener("beforeunload", onBeforeUnload);
    if (sentTimer !== null) window.clearTimeout(sentTimer);
    abandon().catch(fail);
  });

  return {
    phase,
    error,
    sent,
    elapsedMs,
    levels,
    current,
    pending,
    startsConversation,
    start: () => start().catch(fail),
    stop: () => stop().catch(fail),
    cancel: () => cancel().catch(fail),
    retry: (session: ReviewSession) => retry(session).catch(fail),
    discard: (session: ReviewSession) => discard(session).catch(fail),
    download: (session: ReviewSession) => download(session).catch(fail),
    dismissError: () => (error.value = ""),
    refresh: () => refreshPending().catch(fail),
    abandon: () => abandon().catch(fail),
    note,
  };
}
