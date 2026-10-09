// Browser side of a GPT-Live WebRTC session: microphone up, model audio down,
// JSON events on the "oai-events" data channel. The server only brokers the
// SDP exchange (api.createLiveSession); media and events go straight to OpenAI.
import { api } from "./api";

export interface LiveEvent {
  type: string;
  [key: string]: unknown;
}

const ICE_TIMEOUT_MS = 10_000;
const START_TIMEOUT_MS = 15_000;
const CLOSE_TIMEOUT_MS = 15_000;

function withTimeout<T>(promise: Promise<T>, ms: number, what: string): Promise<T> {
  return new Promise<T>((resolve, reject) => {
    const timer = setTimeout(() => reject(new Error(`Timed out ${what}`)), ms);
    promise.then(
      (value) => {
        clearTimeout(timer);
        resolve(value);
      },
      (err) => {
        clearTimeout(timer);
        reject(err);
      },
    );
  });
}

export class LiveSession {
  private closed = false;
  private closing = false;
  private closeTimer: ReturnType<typeof setTimeout> | undefined;

  private constructor(
    private readonly pc: RTCPeerConnection,
    private readonly events: RTCDataChannel,
    private readonly mic: MediaStream,
    private readonly audio: HTMLAudioElement,
    private readonly onClosed: (error?: Error) => void,
  ) {}

  // Connects and resolves once the server reports session.started. onEvent
  // receives every event (including session.started) from then on. Aborting
  // the signal while connecting releases the microphone and rejects.
  static async start(
    conversationId: string,
    onEvent: (event: LiveEvent) => void,
    onClosed: (error?: Error) => void,
    signal: AbortSignal,
  ): Promise<LiveSession> {
    const mic = await navigator.mediaDevices.getUserMedia({
      audio: { echoCancellation: true, noiseSuppression: true },
    });
    if (signal.aborted) {
      for (const t of mic.getTracks()) t.stop();
      throw new Error("Cancelled");
    }
    const pc = new RTCPeerConnection();
    const audio = new Audio();
    audio.autoplay = true;
    pc.ontrack = (e) => {
      audio.srcObject = e.streams[0] ?? new MediaStream([e.track]);
    };
    for (const track of mic.getAudioTracks()) pc.addTrack(track, mic);
    const events = pc.createDataChannel("oai-events");
    // Ends the connect phase early: user abort, or the session dying before it started.
    let abortStart: (err: Error) => void;
    const cancelled = new Promise<never>((_, reject) => (abortStart = reject));
    signal.addEventListener("abort", () => abortStart(new Error("Cancelled")), { once: true });
    const session = new LiveSession(pc, events, mic, audio, (err) => {
      abortStart(err ?? new Error("Live session closed"));
      onClosed(err);
    });

    let markStarted: () => void;
    const started = new Promise<void>((resolve) => (markStarted = resolve));
    events.onmessage = (e) => {
      let event: LiveEvent;
      try {
        event = JSON.parse(e.data as string) as LiveEvent;
      } catch {
        session.finish(new Error("Live session sent a malformed event"));
        return;
      }
      if (event.type === "session.started") markStarted();
      onEvent(event);
    };
    events.onclose = () => session.finish();
    pc.onconnectionstatechange = () => {
      if (pc.connectionState === "failed") session.finish(new Error("Live connection failed"));
    };

    // The race below stops waiting on abort but not connect() itself, so every
    // step checks the signal: no session may be requested after a stop.
    const connect = async () => {
      await pc.setLocalDescription(await pc.createOffer());
      signal.throwIfAborted();
      await withTimeout(
        new Promise<void>((resolve) => {
          if (pc.iceGatheringState === "complete") return resolve();
          pc.addEventListener("icegatheringstatechange", () => {
            if (pc.iceGatheringState === "complete") resolve();
          });
        }),
        ICE_TIMEOUT_MS,
        "gathering network candidates",
      );
      signal.throwIfAborted();
      const answer = await api.createLiveSession(conversationId, pc.localDescription!.sdp, signal);
      signal.throwIfAborted();
      await pc.setRemoteDescription({ type: "answer", sdp: answer.transport.sdp });
      await withTimeout(started, START_TIMEOUT_MS, "waiting for the live session to start");
    };
    try {
      await Promise.race([connect(), cancelled]);
    } catch (err) {
      session.release();
      throw err;
    }
    return session;
  }

  get muted(): boolean {
    return this.mic.getAudioTracks().every((t) => !t.enabled);
  }

  setMuted(muted: boolean): void {
    for (const t of this.mic.getAudioTracks()) t.enabled = !muted;
  }

  send(event: LiveEvent): void {
    if (this.closed || this.closing || this.events.readyState !== "open") {
      throw new Error("Live session is not connected");
    }
    this.events.send(JSON.stringify(event));
  }

  // User-initiated stop. The microphone and speaker go quiet immediately; the
  // transport stays up until the server acknowledges with session.closed (or
  // drops the channel, or CLOSE_TIMEOUT_MS passes) so session.close is not
  // lost to an early teardown. No events reach the caller after this.
  close(): void {
    if (this.closed || this.closing) return;
    if (this.events.readyState !== "open") return this.release();
    this.closing = true;
    for (const t of this.mic.getTracks()) t.stop();
    this.audio.muted = true;
    this.pc.onconnectionstatechange = null;
    this.events.onclose = () => this.release();
    this.events.onmessage = (e) => {
      try {
        if ((JSON.parse(e.data as string) as LiveEvent).type === "session.closed") this.release();
      } catch {
        // Ignore anything unparseable while waiting for the acknowledgement.
      }
    };
    this.closeTimer = setTimeout(() => this.release(), CLOSE_TIMEOUT_MS);
    this.events.send(JSON.stringify({ type: "session.close" }));
  }

  private finish(error?: Error): void {
    if (this.closed || this.closing) return;
    this.release();
    this.onClosed(error);
  }

  private release(): void {
    this.closed = true;
    clearTimeout(this.closeTimer);
    this.events.onmessage = null;
    this.events.onclose = null;
    this.pc.onconnectionstatechange = null;
    this.events.close();
    this.pc.close();
    for (const t of this.mic.getTracks()) t.stop();
    this.audio.srcObject = null;
  }
}
