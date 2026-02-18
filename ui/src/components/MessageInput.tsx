import React, { useState, useRef, useEffect, useCallback, useMemo } from "react";

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
  disabled?: boolean;
  autoFocus?: boolean;
  onFocus?: () => void;
  injectedText?: string;
  onClearInjectedText?: () => void;
  /** If set, persist draft message to localStorage under this key */
  persistKey?: string;
}

const PERSIST_KEY_PREFIX = "shelley_draft_";

function MessageInput({
  onSend,
  disabled = false,
  autoFocus = false,
  onFocus,
  injectedText,
  onClearInjectedText,
  persistKey,
}: MessageInputProps) {
  const [message, setMessage] = useState(() => {
    // Load persisted draft if persistKey is set
    if (persistKey) {
      return localStorage.getItem(PERSIST_KEY_PREFIX + persistKey) || "";
    }
    return "";
  });
  const [submitting, setSubmitting] = useState(false);
  const [uploadsInProgress, setUploadsInProgress] = useState(0);
  const [dragCounter, setDragCounter] = useState(0);
  const [isListening, setIsListening] = useState(false);
  const [isSmallScreen, setIsSmallScreen] = useState(() => {
    if (typeof window === "undefined") return false;
    return window.innerWidth < 480;
  });
  const textareaRef = useRef<HTMLTextAreaElement>(null);
  const fileInputRef = useRef<HTMLInputElement>(null);
  const recognitionRef = useRef<SpeechRecognition | null>(null);
  // Track the base text (before speech recognition started) and finalized speech text
  const baseTextRef = useRef<string>("");
  const finalizedTextRef = useRef<string>("");

  // Check if speech recognition is available
  const speechRecognitionAvailable =
    typeof window !== "undefined" && (window.SpeechRecognition || window.webkitSpeechRecognition);

  // Responsive placeholder text
  const placeholderText = useMemo(
    () => (isSmallScreen ? "Message..." : "Message, paste image, or attach file..."),
    [isSmallScreen],
  );

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

  const uploadFile = async (file: File) => {
    // Add a loading indicator at the end of the current message
    const loadingText = `[uploading ${file.name}...]`;
    setMessage((prev) => (prev ? prev + " " : "") + loadingText);
    setUploadsInProgress((prev) => prev + 1);

    try {
      const formData = new FormData();
      formData.append("file", file);

      const response = await fetch("/api/upload", {
        method: "POST",
        body: formData,
      });

      if (!response.ok) {
        throw new Error(`Upload failed: ${response.statusText}`);
      }

      const data = await response.json();

      // Replace the loading placeholder with the actual file path
      setMessage((currentMessage) => currentMessage.replace(loadingText, `[${data.path}]`));
    } catch (error) {
      console.error("Failed to upload file:", error);
      // Replace loading indicator with error message
      const errorText = `[upload failed: ${error instanceof Error ? error.message : "unknown error"}]`;
      setMessage((currentMessage) => currentMessage.replace(loadingText, errorText));
    } finally {
      setUploadsInProgress((prev) => prev - 1);
    }
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

    if (event.dataTransfer && event.dataTransfer.files.length > 0) {
      // Process all dropped files
      for (let i = 0; i < event.dataTransfer.files.length; i++) {
        const file = event.dataTransfer.files[i];
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

  const handleSubmit = async (e: React.FormEvent) => {
    e.preventDefault();
    if (message.trim() && !disabled && !submitting && uploadsInProgress === 0) {
      // Stop listening if we were recording
      if (isListening) {
        stopListening();
      }

      const messageToSend = message;
      setSubmitting(true);
      try {
        await onSend(messageToSend);
        // Only clear on success
        setMessage("");
        // Clear persisted draft on successful send
        if (persistKey) {
          localStorage.removeItem(PERSIST_KEY_PREFIX + persistKey);
        }
      } catch {
        // Keep the message on error so user can retry
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
    if (e.key === "Enter" && !e.shiftKey) {
      // On mobile, let Enter create newlines since there's a send button
      // I'm not convinced the divergence from desktop is the correct answer,
      // but we can try it and see how it feels.
      const isMobile = "ontouchstart" in window;
      if (isMobile) {
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

  // Persist draft to localStorage when persistKey is set
  useEffect(() => {
    if (persistKey) {
      if (message) {
        localStorage.setItem(PERSIST_KEY_PREFIX + persistKey, message);
      } else {
        localStorage.removeItem(PERSIST_KEY_PREFIX + persistKey);
      }
    }
  }, [message, persistKey]);

  useEffect(() => {
    if (autoFocus && textareaRef.current) {
      // Use setTimeout to ensure the component is fully rendered
      setTimeout(() => {
        textareaRef.current?.focus();
      }, 0);
    }
  }, [autoFocus]);

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
  const canSubmit = message.trim() && !isDisabled && !submitting && uploadsInProgress === 0;

  const isDraggingOver = dragCounter > 0;
  // Check if user is typing a shell command (starts with !)
  const isShellMode = message.trimStart().startsWith("!");
  // Note: injectedText is auto-inserted via useEffect, no manual UI needed

  return (
    <div
      className={`message-input-container ${isDraggingOver ? "drag-over" : ""} ${isShellMode ? "shell-mode" : ""}`}
      onDragOver={handleDragOver}
      onDragEnter={handleDragEnter}
      onDragLeave={handleDragLeave}
      onDrop={handleDrop}
    >
      {isDraggingOver && (
        <div className="drag-overlay">
          <div className="drag-overlay-content">Drop files here</div>
        </div>
      )}
      <form onSubmit={handleSubmit} className="message-input-form">
        <input
          type="file"
          ref={fileInputRef}
          onChange={handleFileSelect}
          style={{ display: "none" }}
          multiple
          accept="image/*,video/*,audio/*,.pdf,.txt,.md,.json,.csv,.xml,.html,.css,.js,.ts,.tsx,.jsx,.py,.go,.rs,.java,.c,.cpp,.h,.hpp,.sh,.yaml,.yml,.toml,.sql,.log,*"
          aria-hidden="true"
        />
        <div className="textarea-wrapper">
          {isShellMode && (
            <div className="shell-mode-indicator" title="This will run as a shell command">
              <svg
                width="16"
                height="16"
                viewBox="0 0 24 24"
                fill="none"
                stroke="currentColor"
                strokeWidth="2"
              >
                <polyline points="4 17 10 11 4 5" />
                <line x1="12" y1="19" x2="20" y2="19" />
              </svg>
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
            className="message-textarea"
            disabled={isDisabled}
            rows={1}
            aria-label="Message input"
            data-testid="message-input"
            autoFocus={autoFocus}
          />
        </div>
        <button
          type="button"
          onClick={handleAttachClick}
          disabled={isDisabled}
          className="message-attach-btn"
          aria-label="Attach file"
          data-testid="attach-button"
        >
          <svg
            fill="none"
            stroke="currentColor"
            strokeWidth="2"
            viewBox="0 0 24 24"
            width="20"
            height="20"
          >
            <path
              strokeLinecap="round"
              strokeLinejoin="round"
              d="M15.172 7l-6.586 6.586a2 2 0 102.828 2.828l6.414-6.586a4 4 0 00-5.656-5.656l-6.415 6.585a6 6 0 108.486 8.486L20.5 13"
            />
          </svg>
        </button>
        {speechRecognitionAvailable && (
          <button
            type="button"
            onClick={toggleListening}
            disabled={isDisabled}
            className={`message-voice-btn ${isListening ? "listening" : ""}`}
            aria-label={isListening ? "Stop voice input" : "Start voice input"}
            data-testid="voice-button"
          >
            {isListening ? (
              <svg fill="currentColor" viewBox="0 0 24 24" width="20" height="20">
                <circle cx="12" cy="12" r="6" />
              </svg>
            ) : (
              <svg fill="currentColor" viewBox="0 0 24 24" width="20" height="20">
                <path d="M12 14c1.66 0 3-1.34 3-3V5c0-1.66-1.34-3-3-3S9 3.34 9 5v6c0 1.66 1.34 3 3 3zm-1-9c0-.55.45-1 1-1s1 .45 1 1v6c0 .55-.45 1-1 1s-1-.45-1-1V5zm6 6c0 2.76-2.24 5-5 5s-5-2.24-5-5H5c0 3.53 2.61 6.43 6 6.92V21h2v-3.08c3.39-.49 6-3.39 6-6.92h-2z" />
              </svg>
            )}
          </button>
        )}
        <button
          type="submit"
          disabled={!canSubmit}
          className="message-send-btn"
          aria-label="Send message"
          data-testid="send-button"
        >
          {isDisabled || submitting ? (
            <div className="flex items-center justify-center">
              <div className="spinner spinner-small" style={{ borderTopColor: "white" }}></div>
            </div>
          ) : (
            <svg fill="currentColor" viewBox="0 0 24 24" width="20" height="20">
              <path d="M12 4l-1.41 1.41L16.17 11H4v2h12.17l-5.58 5.59L12 20l8-8z" />
            </svg>
          )}
        </button>
      </form>
    </div>
  );
}

export default MessageInput;
