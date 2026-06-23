import React, { useState, useRef, useEffect, useCallback, useMemo } from "react";
import {
  Paperclip,
  Mic,
  Square,
  ChevronDown,
  Clock,
  Loader2,
  X,
  FileIcon,
  TerminalIcon,
  SendHorizontal,
} from "lucide-react";
import { useI18n } from "../i18n";
import { pickPlaceholderHint } from "../utils/placeholderHints";
import { cn } from "@/lib/utils";

// Web Speech API types
interface SpeechRecognitionEvent extends Event {
  results: SpeechRecognitionResultList;
  resultIndex: number;
}

interface SpeechRecognitionResultList {
  length: number;
  item(index: number): SpeechRecognitionResult;
  [index: number]: SpeechRecognitionResult;
}

interface SpeechRecognitionResult {
  isFinal: boolean;
  length: number;
  item(index: number): SpeechRecognitionAlternative;
  [index: number]: SpeechRecognitionAlternative;
}

interface SpeechRecognitionAlternative {
  transcript: string;
  confidence: number;
}

interface SpeechRecognition extends EventTarget {
  continuous: boolean;
  interimResults: boolean;
  lang: string;
  onresult: ((event: SpeechRecognitionEvent) => void) | null;
  onerror: ((event: Event & { error: string }) => void) | null;
  onend: (() => void) | null;
  start(): void;
  stop(): void;
  abort(): void;
}

declare global {
  interface Window {
    SpeechRecognition: new () => SpeechRecognition;
    webkitSpeechRecognition: new () => SpeechRecognition;
  }
}

interface MessageInputProps {
  onSend: (message: string) => Promise<void>;
  onQueue?: (message: string) => Promise<void>;
  /** Show the split send button with queue chevron (e.g. when in a conversation) */
  showQueueOption?: boolean;
  /** Whether queuing is available right now (agent is working) */
  canQueue?: boolean;
  /** Auto-queue instead of sending (e.g. when distilling) */
  autoQueue?: boolean;
  disabled?: boolean;
  autoFocus?: boolean;
  onFocus?: () => void;
  injectedText?: string;
  onClearInjectedText?: () => void;
  /** Controlled draft text. When provided, MessageInput surfaces every
   * keystroke via onDraftChange so the parent can persist it (server-side
   * autosave for draft conversations). */
  draftValue?: string;
  onDraftChange?: (value: string) => void;
  /** Pause autosave — called the instant the user hits send so the
   * trailing PUT doesn't race the chat POST. The parent should drop any
   * pending timer but leave its draftValue alone; the textarea retains
   * the in-flight message so a failed send doesn't lose user input. */
  onDraftSendStarted?: () => void;
  /** Called once the message has been successfully sent so the parent can
   * tear down any remaining autosave state. */
  onDraftCleared?: () => void;
  initialRows?: number;
  /** Status bar content rendered inline on mobile (hidden on desktop) */
  statusSlot?: React.ReactNode;
}

interface Attachment {
  id: string;
  name: string;
  isImage: boolean;
  /** Object URL for image preview thumbnail; revoked on remove/unmount. */
  previewUrl?: string;
  status: "uploading" | "ready" | "error";
  /** Server-returned path; only present once status === "ready". */
  path?: string;
  error?: string;
}

function MessageInput({
  onSend,
  onQueue,
  showQueueOption = false,
  canQueue = false,
  autoQueue = false,
  disabled = false,
  autoFocus = false,
  onFocus,
  injectedText,
  onClearInjectedText,
  draftValue,
  onDraftChange,
  onDraftSendStarted,
  onDraftCleared,
  initialRows = 1,
  statusSlot,
}: MessageInputProps) {
  const { t } = useI18n();
  const [message, setMessageState] = useState(draftValue ?? "");
  const setMessage = useCallback(
    (next: string | ((prev: string) => string)) => {
      setMessageState((prev) => {
        const value = typeof next === "function" ? next(prev) : next;
        if (onDraftChange && value !== prev) onDraftChange(value);
        return value;
      });
    },
    [onDraftChange],
  );
  // Sync external draft updates (e.g. when switching between draft
  // conversations) into local state without losing focus.
  useEffect(() => {
    if (draftValue !== undefined) setMessageState(draftValue);
    // We intentionally re-sync only when the *external* value changes; the
    // setMessage path above already keeps the controlled value in sync
    // for user edits.
  }, [draftValue]);
  const [submitting, setSubmitting] = useState(false);
  const [attachments, setAttachments] = useState<Attachment[]>([]);
  const uploadsInProgress = attachments.filter((a) => a.status === "uploading").length;
  const readyAttachments = attachments.filter((a) => a.status === "ready" && a.path);
  const [dragCounter, setDragCounter] = useState(0);
  const [isListening, setIsListening] = useState(false);
  const [isSmallScreen, setIsSmallScreen] = useState(() => {
    if (typeof window === "undefined") return false;
    return window.innerWidth < 480;
  });
  const [showQueueMenu, setShowQueueMenu] = useState(false);
  const queueMenuRef = useRef<HTMLDivElement>(null);
  const textareaRef = useRef<HTMLTextAreaElement>(null);
  const fileInputRef = useRef<HTMLInputElement>(null);
  const recognitionRef = useRef<SpeechRecognition | null>(null);
  // Track the base text (before speech recognition started) and finalized speech text
  const baseTextRef = useRef<string>("");
  const finalizedTextRef = useRef<string>("");

  // Check if speech recognition is available
  const speechRecognitionAvailable =
    typeof window !== "undefined" && (window.SpeechRecognition || window.webkitSpeechRecognition);

  // Pick a placeholder hint per mount; re-pick when the platform (mobile/desktop) flips.
  const [hint, setHint] = useState(() => pickPlaceholderHint(isSmallScreen));
  const initialPlatformRef = useRef(isSmallScreen);
  useEffect(() => {
    if (isSmallScreen === initialPlatformRef.current) return; // skip initial mount; useState already rolled
    initialPlatformRef.current = isSmallScreen;
    setHint(pickPlaceholderHint(isSmallScreen));
  }, [isSmallScreen]);

  // Responsive placeholder text. The "default" hint defers to the i18n string;
  // other hints carry literal text and are not translated.
  const placeholderText = useMemo(() => {
    if (hint.id !== "default" && hint.text) return hint.text;
    return isSmallScreen ? t("messagePlaceholderShort") : t("messagePlaceholder");
  }, [hint, isSmallScreen, t]);

  // Track screen size for responsive placeholder
  useEffect(() => {
    const handleResize = () => {
      setIsSmallScreen(window.innerWidth < 480);
    };
    window.addEventListener("resize", handleResize);
    return () => window.removeEventListener("resize", handleResize);
  }, []);

  const stopListening = useCallback(() => {
    if (recognitionRef.current) {
      recognitionRef.current.stop();
      recognitionRef.current = null;
    }
    setIsListening(false);
  }, []);

  const startListening = useCallback(() => {
    if (!speechRecognitionAvailable) return;

    const SpeechRecognitionClass = window.SpeechRecognition || window.webkitSpeechRecognition;
    const recognition = new SpeechRecognitionClass();

    recognition.continuous = true;
    recognition.interimResults = true;
    recognition.lang = navigator.language || "en-US";

    // Capture current message as base text
    setMessage((current) => {
      baseTextRef.current = current;
      finalizedTextRef.current = "";
      return current;
    });

    recognition.onresult = (event: SpeechRecognitionEvent) => {
      let finalTranscript = "";
      let interimTranscript = "";

      for (let i = event.resultIndex; i < event.results.length; i++) {
        const transcript = event.results[i][0].transcript;
        if (event.results[i].isFinal) {
          finalTranscript += transcript;
        } else {
          interimTranscript += transcript;
        }
      }

      // Accumulate finalized text
      if (finalTranscript) {
        finalizedTextRef.current += finalTranscript;
      }

      // Build the full message: base + finalized + interim
      const base = baseTextRef.current;
      const needsSpace = base.length > 0 && !/\s$/.test(base);
      const spacer = needsSpace ? " " : "";
      const fullText = base + spacer + finalizedTextRef.current + interimTranscript;

      setMessage(fullText);
    };

    recognition.onerror = (event) => {
      console.error("Speech recognition error:", event.error);
      stopListening();
    };

    recognition.onend = () => {
      setIsListening(false);
      recognitionRef.current = null;
    };

    recognitionRef.current = recognition;
    recognition.start();
    setIsListening(true);
  }, [speechRecognitionAvailable, stopListening]);

  const toggleListening = useCallback(() => {
    if (isListening) {
      stopListening();
    } else {
      startListening();
    }
  }, [isListening, startListening, stopListening]);

  // Cleanup on unmount
  useEffect(() => {
    return () => {
      if (recognitionRef.current) {
        recognitionRef.current.abort();
      }
    };
  }, []);

  // Close queue menu on click outside
  useEffect(() => {
    if (!showQueueMenu) return;
    const handler = (e: MouseEvent) => {
      if (queueMenuRef.current && !queueMenuRef.current.contains(e.target as Node)) {
        setShowQueueMenu(false);
      }
    };
    document.addEventListener("mousedown", handler);
    return () => document.removeEventListener("mousedown", handler);
  }, [showQueueMenu]);

  // Close queue menu when queueing becomes unavailable
  useEffect(() => {
    if (!canQueue && !autoQueue) setShowQueueMenu(false);
  }, [canQueue, autoQueue]);

  const uploadFile = async (file: File) => {
    const id = `${Date.now()}-${Math.random().toString(36).slice(2)}`;
    const isImage = file.type.startsWith("image/");
    const previewUrl = isImage ? URL.createObjectURL(file) : undefined;
    setAttachments((prev) => [
      ...prev,
      { id, name: file.name, isImage, previewUrl, status: "uploading" },
    ]);

    try {
      const formData = new FormData();
      formData.append("file", file);

      const response = await fetch("/api/upload", {
        method: "POST",
        body: formData,
      });

      if (!response.ok) {
        const errorText = await response.text();
        let message = response.statusText;
        if (errorText.trim()) {
          try {
            const payload = JSON.parse(errorText) as { message?: unknown };
            if (typeof payload.message === "string" && payload.message.trim()) {
              message = payload.message.trim();
            }
          } catch {
            message = errorText.trim();
          }
        }
        throw new Error(`Upload failed: ${message}`);
      }

      const data = await response.json();

      setAttachments((prev) =>
        prev.map((a) => (a.id === id ? { ...a, status: "ready", path: data.path } : a)),
      );
    } catch (error) {
      console.error("Failed to upload file:", error);
      const msg = error instanceof Error ? error.message : "unknown error";
      setAttachments((prev) =>
        prev.map((a) => (a.id === id ? { ...a, status: "error", error: msg } : a)),
      );
    }
  };

  const removeAttachment = (id: string) => {
    setAttachments((prev) => {
      const found = prev.find((a) => a.id === id);
      if (found?.previewUrl) URL.revokeObjectURL(found.previewUrl);
      return prev.filter((a) => a.id !== id);
    });
  };

  // Revoke any remaining object URLs on unmount. We track current attachments
  // through a ref so the unmount cleanup sees the latest list (a plain []
  // deps useEffect would close over the initial empty array and leak URLs).
  const attachmentsRef = useRef(attachments);
  useEffect(() => {
    attachmentsRef.current = attachments;
  }, [attachments]);
  useEffect(() => {
    return () => {
      attachmentsRef.current.forEach((a) => {
        if (a.previewUrl) URL.revokeObjectURL(a.previewUrl);
      });
    };
  }, []);

  /**
   * Compose the final message text by appending `[path]` tokens for each
   * ready attachment. Used at send time; thumbnails are shown until then.
   */
  const composeMessageWithAttachments = (text: string): string => {
    if (readyAttachments.length === 0) return text;
    const tokens = readyAttachments.map((a) => `[${a.path}]`).join(" ");
    const trimmed = text.trimEnd();
    return trimmed.length > 0 ? `${trimmed} ${tokens}` : tokens;
  };

  const clearAttachments = () => {
    attachments.forEach((a) => {
      if (a.previewUrl) URL.revokeObjectURL(a.previewUrl);
    });
    setAttachments([]);
  };

  const handlePaste = (event: React.ClipboardEvent) => {
    // Check clipboard items (works on both desktop and mobile)
    // Mobile browsers often don't populate clipboardData.files, but items works
    const items = event.clipboardData?.items;
    if (items) {
      for (let i = 0; i < items.length; i++) {
        const item = items[i];
        if (item.kind === "file") {
          const file = item.getAsFile();
          if (file) {
            event.preventDefault();
            // Fire and forget - uploadFile handles state updates internally.
            uploadFile(file);
            return;
          }
        }
      }
    }
  };

  const handleDragOver = (event: React.DragEvent) => {
    event.preventDefault();
    event.stopPropagation();
  };

  const handleDragEnter = (event: React.DragEvent) => {
    event.preventDefault();
    event.stopPropagation();
    setDragCounter((prev) => prev + 1);
  };

  const handleDragLeave = (event: React.DragEvent) => {
    event.preventDefault();
    event.stopPropagation();
    setDragCounter((prev) => prev - 1);
  };

  const handleDrop = async (event: React.DragEvent) => {
    event.preventDefault();
    event.stopPropagation();
    setDragCounter(0);

    // Snapshot the file list synchronously. After the first `await`, the
    // DataTransfer enters "protected mode" and `event.dataTransfer.files`
    // becomes empty, so iterating over it across awaits would only ever
    // upload the first file.
    if (event.dataTransfer && event.dataTransfer.files.length > 0) {
      const files = Array.from(event.dataTransfer.files);
      for (const file of files) {
        await uploadFile(file);
      }
    }
  };

  const handleAttachClick = () => {
    fileInputRef.current?.click();
  };

  const handleFileSelect = async (event: React.ChangeEvent<HTMLInputElement>) => {
    const files = event.target.files;
    if (!files || files.length === 0) return;

    for (let i = 0; i < files.length; i++) {
      const file = files[i];
      await uploadFile(file);
    }

    // Reset input so same file can be selected again
    event.target.value = "";
  };

  // Auto-insert injected text (diff comments) directly into the textarea
  useEffect(() => {
    if (injectedText) {
      setMessage((prev) => {
        const needsNewline = prev.length > 0 && !prev.endsWith("\n");
        return prev + (needsNewline ? "\n\n" : "") + injectedText;
      });
      onClearInjectedText?.();
      // Focus the textarea after inserting
      setTimeout(() => textareaRef.current?.focus(), 0);
    }
  }, [injectedText, onClearInjectedText]);

  const hasContent = message.trim().length > 0 || readyAttachments.length > 0;

  const handleSubmit = async (e: React.FormEvent) => {
    e.preventDefault();
    if (hasContent && !disabled && !submitting && uploadsInProgress === 0) {
      // Stop listening if we were recording
      if (isListening) {
        stopListening();
      }

      // Auto-queue when distilling or when explicitly requested
      if (autoQueue && onQueue) {
        const messageToQueue = composeMessageWithAttachments(message).trim();
        setMessage("");
        clearAttachments();
        onDraftCleared?.();
        try {
          await onQueue(messageToQueue);
        } catch {
          setMessage(messageToQueue);
        }
        return;
      }

      const messageToSend = composeMessageWithAttachments(message);
      // Pause autosave before awaiting onSend so a trailing PUT can't
      // race the chat POST (404 after promote is harmless but noisy).
      // We don't clear the draft yet — if the send fails, the textarea
      // (and the server-side draft body) stay intact for retry.
      onDraftSendStarted?.();
      setSubmitting(true);
      try {
        await onSend(messageToSend);
        // Only clear on success
        setMessage("");
        clearAttachments();
        onDraftCleared?.();
      } catch {
        // Keep the message on error so user can retry.
      } finally {
        setSubmitting(false);
      }
    }
  };

  const handleQueueMessage = async () => {
    if (hasContent && onQueue) {
      if (isListening) {
        stopListening();
      }
      const messageToQueue = composeMessageWithAttachments(message).trim();
      setMessage("");
      clearAttachments();
      onDraftCleared?.();
      setShowQueueMenu(false);
      try {
        await onQueue(messageToQueue);
      } catch {
        // Restore message on failure
        setMessage(messageToQueue);
      }
    }
  };

  /** Send now (bypass auto-queue) — used from the dropdown during distill mode */
  const handleSendNow = async () => {
    if (hasContent && !disabled && !submitting && uploadsInProgress === 0) {
      if (isListening) {
        stopListening();
      }
      const messageToSend = composeMessageWithAttachments(message).trim();
      setMessage("");
      clearAttachments();
      onDraftCleared?.();
      setShowQueueMenu(false);
      setSubmitting(true);
      try {
        await onSend(messageToSend);
      } catch {
        setMessage(messageToSend);
      } finally {
        setSubmitting(false);
      }
    }
  };

  const handleKeyDown = (e: React.KeyboardEvent) => {
    // Don't submit while IME is composing (e.g., converting Japanese hiragana to kanji)
    if (e.nativeEvent.isComposing) {
      return;
    }
    // Escape blurs the textarea, so that follow-up shortcuts like
    // Cmd+ArrowDown (scroll conversation to bottom) work without
    // having to click out of the input first.
    if (e.key === "Escape") {
      textareaRef.current?.blur();
      return;
    }
    if (e.key === "Enter" && !e.shiftKey) {
      // On mobile, let Enter create newlines since there's a send button
      // I'm not convinced the divergence from desktop is the correct answer,
      // but we can try it and see how it feels.
      const isMobile = "ontouchstart" in window;
      if (isMobile && !(e.ctrlKey || e.metaKey)) {
        return;
      }
      e.preventDefault();
      handleSubmit(e);
    }
  };

  const adjustTextareaHeight = () => {
    if (textareaRef.current) {
      textareaRef.current.style.height = "auto";
      const scrollHeight = textareaRef.current.scrollHeight;
      const maxHeight = 200; // Maximum height in pixels
      textareaRef.current.style.height = `${Math.min(scrollHeight, maxHeight)}px`;
    }
  };

  useEffect(() => {
    adjustTextareaHeight();
  }, [message]);

  // Re-focus textarea after submission completes and it's re-enabled.
  // Only restore focus if it fell to document.body (i.e. the user didn't
  // deliberately move focus elsewhere while the message was submitting).
  // Skip on mobile to avoid popping up the soft keyboard unexpectedly.
  useEffect(() => {
    const isMobile = "ontouchstart" in window;
    if (!submitting && !isMobile && document.activeElement === document.body) {
      textareaRef.current?.focus();
    }
  }, [submitting]);

  useEffect(() => {
    // Guard on !disabled (and depend on disabled) so focus is re-attempted
    // when the textarea becomes enabled — on page load it's briefly disabled
    // while messages load.
    if (autoFocus && !disabled && textareaRef.current) {
      // Use setTimeout to ensure the component is fully rendered
      setTimeout(() => {
        textareaRef.current?.focus();
      }, 0);
    }
  }, [autoFocus, disabled]);

  // Handle virtual keyboard appearance on mobile (especially Android Firefox)
  // The visualViewport API lets us detect when the keyboard shrinks the viewport
  useEffect(() => {
    if (typeof window === "undefined" || !window.visualViewport) {
      return;
    }

    const handleViewportResize = () => {
      // Only scroll if our textarea is focused (keyboard is for us)
      if (document.activeElement === textareaRef.current) {
        // Small delay to let the viewport settle after resize
        requestAnimationFrame(() => {
          textareaRef.current?.scrollIntoView({ behavior: "smooth", block: "center" });
        });
      }
    };

    window.visualViewport.addEventListener("resize", handleViewportResize);
    return () => {
      window.visualViewport?.removeEventListener("resize", handleViewportResize);
    };
  }, []);

  const isDisabled = disabled;
  const canSubmit = hasContent && !isDisabled && !submitting && uploadsInProgress === 0;

  const isDraggingOver = dragCounter > 0;
  // Check if user is typing a shell command (starts with !)
  const isShellMode = message.trimStart().startsWith("!");
  // Note: injectedText is auto-inserted via useEffect, no manual UI needed

  return (
    <div
      className={cn(
        "message-input-container relative flex-none border-t border-border bg-background px-4 pt-4",
        "pb-[max(1rem,env(safe-area-inset-bottom))] pl-[max(1rem,env(safe-area-inset-left))] pr-[max(1rem,env(safe-area-inset-right))]",
        "md:px-4 max-md:px-3",
        isDraggingOver && "border-primary",
      )}
      onDragOver={handleDragOver}
      onDragEnter={handleDragEnter}
      onDragLeave={handleDragLeave}
      onDrop={handleDrop}
    >
      {isDraggingOver && (
        <div className="drag-overlay pointer-events-none absolute inset-0 z-10 flex items-center justify-center rounded-md border-2 border-dashed border-primary bg-primary/10">
          <div className="rounded-md bg-primary px-4 py-2 text-sm font-medium text-primary-foreground">
            {t("dropFilesHere")}
          </div>
        </div>
      )}
      <form
        onSubmit={handleSubmit}
        className="mx-auto flex max-w-[800px] flex-wrap items-end gap-2 max-md:flex-col-reverse max-md:items-stretch max-md:gap-1.5"
      >
        <input
          type="file"
          ref={fileInputRef}
          onChange={handleFileSelect}
          className="message-input-hidden hidden"
          multiple
          aria-hidden="true"
        />
        {attachments.length > 0 && (
          <div
            className="order-first flex w-full flex-wrap gap-2 pb-2 pt-1"
            data-testid="message-attachments"
          >
            {attachments.map((a) => (
              <div
                key={a.id}
                className={cn(
                  "message-attachment relative size-16 shrink-0 overflow-hidden rounded-md border bg-muted",
                  a.status === "error" ? "border-destructive/60" : "border-border",
                )}
                title={a.status === "error" ? `${a.name}: ${a.error}` : a.name}
              >
                {a.isImage && a.previewUrl ? (
                  <img
                    src={a.previewUrl}
                    alt={a.name}
                    className="block size-full object-cover"
                  />
                ) : (
                  <div className="flex size-full flex-col items-center justify-center gap-0.5 p-1 text-center text-muted-foreground">
                    <FileIcon className="size-5" />
                    <span className="max-w-full truncate text-[0.65rem]">{a.name}</span>
                  </div>
                )}
                {a.status === "uploading" && (
                  <div className="absolute inset-0 flex items-center justify-center bg-black/45">
                    <Loader2 className="size-4 animate-spin text-white" />
                  </div>
                )}
                {a.status === "error" && (
                  <div className="absolute inset-0 flex items-center justify-center bg-destructive/70 text-xl font-bold text-white">
                    !
                  </div>
                )}
                <button
                  type="button"
                  className="absolute right-0.5 top-0.5 flex size-[18px] items-center justify-center rounded-full bg-black/60 p-0.5 text-white opacity-85 transition-opacity hover:bg-black/80 hover:opacity-100"
                  onClick={() => removeAttachment(a.id)}
                  aria-label={`Remove ${a.name}`}
                >
                  <X className="size-full" />
                </button>
              </div>
            ))}
          </div>
        )}
        <div className="relative min-w-0 flex-1">
          {isShellMode && (
            <div
              className="pointer-events-none absolute left-3 top-3.5 z-[1] flex items-center justify-center text-amber-600 dark:text-amber-500"
              title="This will run as a shell command"
            >
              <TerminalIcon className="size-4" />
            </div>
          )}
          <textarea
            ref={textareaRef}
            value={message}
            onChange={(e) => setMessage(e.target.value)}
            onKeyDown={handleKeyDown}
            onPaste={handlePaste}
            onFocus={() => {
              // Scroll to bottom after keyboard animation settles
              if (onFocus) {
                requestAnimationFrame(() => requestAnimationFrame(onFocus));
              }
            }}
            placeholder={placeholderText}
            className={cn(
              "block max-h-[200px] min-h-[46px] w-full resize-none rounded-md border bg-background px-4 py-2.5 text-base leading-relaxed text-foreground transition-colors placeholder:text-muted-foreground",
              "focus:border-primary focus:outline-none focus:ring-2 focus:ring-ring/30",
              "disabled:cursor-not-allowed disabled:opacity-50",
              isShellMode
                ? "border-amber-400 bg-amber-50/60 pl-9 font-mono dark:border-amber-500/50 dark:bg-amber-950/20"
                : "border-border",
            )}
            disabled={isDisabled}
            rows={initialRows}
            aria-label="Message input"
            data-testid="message-input"
            autoFocus={autoFocus}
          />
        </div>
        <div className="flex items-center justify-end gap-1 max-md:w-full">
          {statusSlot && (
            <div className="mr-auto hidden min-w-0 flex-1 items-center overflow-hidden text-sm max-md:flex">
              {statusSlot}
            </div>
          )}
          <button
            type="button"
            onClick={handleAttachClick}
            disabled={isDisabled}
            className="flex size-9 shrink-0 items-center justify-center rounded-full text-muted-foreground transition-colors hover:bg-accent hover:text-foreground active:scale-95 disabled:cursor-not-allowed disabled:opacity-40 md:mb-1"
            aria-label={t("attachFile")}
            data-testid="attach-button"
          >
            <Paperclip className="size-5" />
          </button>
          {speechRecognitionAvailable && (
            <button
              type="button"
              onClick={toggleListening}
              disabled={isDisabled}
              className={cn(
                "flex size-9 shrink-0 items-center justify-center rounded-full transition-colors active:scale-95 disabled:cursor-not-allowed disabled:opacity-40 md:mb-1",
                isListening
                  ? "animate-pulse bg-red-600 text-white hover:bg-red-600 dark:bg-red-500"
                  : "text-muted-foreground hover:bg-accent hover:text-foreground",
              )}
              aria-label={isListening ? t("stopVoiceInput") : t("startVoiceInput")}
              data-testid="voice-button"
            >
              {isListening ? (
                <Square className="size-5 fill-current" />
              ) : (
                <Mic className="size-5" />
              )}
            </button>
          )}
          <div className="relative flex items-end md:mb-1" ref={queueMenuRef}>
            {showQueueOption && onQueue ? (
              /* Slack-style split button: [Send | ▾] — always same width */
              <div
                className={cn(
                  "flex items-stretch overflow-hidden rounded-full transition-colors",
                  canSubmit
                    ? autoQueue
                      ? "bg-orange-500"
                      : "bg-primary"
                    : "bg-muted opacity-60",
                )}
              >
                <button
                  type="submit"
                  disabled={!canSubmit}
                  className="flex size-9 items-center justify-center text-primary-foreground transition-colors hover:bg-white/15 active:bg-white/25 disabled:cursor-not-allowed disabled:opacity-50"
                  aria-label={autoQueue ? "Queue message" : t("sendMessage")}
                  data-testid="send-button"
                >
                  {isDisabled || submitting ? (
                    <Loader2 className="size-4 animate-spin" />
                  ) : (
                    <SendHorizontal className="size-[18px]" />
                  )}
                </button>
                <div className="my-1.5 w-px bg-white/30" />
                <button
                  type="button"
                  disabled={!canSubmit || (!canQueue && !autoQueue)}
                  className={cn(
                    "flex w-7 items-center justify-center text-primary-foreground transition-colors hover:bg-white/15 active:bg-white/25 disabled:cursor-not-allowed disabled:opacity-50",
                    !canQueue && !autoQueue && "cursor-default opacity-40",
                  )}
                  aria-label="Send options"
                  data-testid="send-options-button"
                  onClick={() => setShowQueueMenu((v) => !v)}
                >
                  <ChevronDown className="size-3.5" />
                </button>
                {showQueueMenu && (canQueue || autoQueue) && (
                  <div className="absolute bottom-[calc(100%+4px)] right-0 z-[100] min-w-[220px] rounded-lg border border-border bg-popover p-1 text-popover-foreground shadow-lg">
                    <button
                      type="button"
                      className="flex w-full items-center gap-2 whitespace-nowrap rounded-md px-3 py-2 text-left text-[0.85rem] transition-colors hover:bg-accent hover:text-accent-foreground"
                      data-testid="queue-option"
                      onClick={autoQueue ? handleSendNow : handleQueueMessage}
                    >
                      {autoQueue ? (
                        /* During distill (autoQueue=true), main button queues, dropdown offers "send now" */
                        <>
                          <SendHorizontal className="size-4 shrink-0 text-muted-foreground" />
                          Send now
                        </>
                      ) : (
                        /* Clock icon — "queue for later" */
                        <>
                          <Clock className="size-4 shrink-0 text-muted-foreground" />
                          Queue after agent finishes
                        </>
                      )}
                    </button>
                  </div>
                )}
              </div>
            ) : (
              /* Regular round send button (new conversation, no queue possible) */
              <button
                type="submit"
                disabled={!canSubmit}
                className="flex size-9 shrink-0 items-center justify-center rounded-full bg-primary text-primary-foreground transition-transform hover:bg-primary/90 hover:not-disabled:scale-105 active:not-disabled:scale-95 disabled:cursor-not-allowed disabled:bg-muted disabled:opacity-60"
                aria-label={t("sendMessage")}
                data-testid="send-button"
              >
                {isDisabled || submitting ? (
                  <Loader2 className="size-4 animate-spin" />
                ) : (
                  <SendHorizontal className="size-5" />
                )}
              </button>
            )}
          </div>
        </div>
      </form>
    </div>
  );
}

export default MessageInput;
