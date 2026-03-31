import { useEffect, useRef, useState } from "react";
import { ThemeMode, setStoredTheme, applyTheme } from "../services/theme";
import type { Locale, TranslationKeys } from "../i18n";

type LinkItem = {
  title: string;
  url: string;
  icon_svg?: string;
};

const LANGUAGE_OPTIONS: { locale: Locale; flag: string; label: string }[] = [
  { locale: "en", flag: "🇺🇸", label: "English" },
  { locale: "ja", flag: "🇯🇵", label: "日本語" },
  { locale: "fr", flag: "🇫🇷", label: "Français" },
  { locale: "ru", flag: "🇷🇺", label: "Русский" },
  { locale: "es", flag: "🇪🇸", label: "Español" },
  { locale: "zh-CN", flag: "🇨🇳", label: "简体中文" },
  { locale: "zh-TW", flag: "🇹🇼", label: "繁體中文" },
  { locale: "upgoer5", flag: "🚀", label: "Up-Goer Five" },
];

function LanguageDropdown({
  locale,
  setLocale,
  t,
}: {
  locale: Locale;
  setLocale: (l: Locale) => void;
  t: (key: keyof TranslationKeys) => string;
}) {
  const [open, setOpen] = useState(false);
  const ref = useRef<HTMLDivElement>(null);
  const current = LANGUAGE_OPTIONS.find((o) => o.locale === locale)!;

  useEffect(() => {
    if (!open) return;
    const handler = (e: MouseEvent) => {
      if (ref.current && !ref.current.contains(e.target as Node)) {
        setOpen(false);
      }
    };
    document.addEventListener("mousedown", handler);
    return () => document.removeEventListener("mousedown", handler);
  }, [open]);

  return (
    <div className="language-dropdown" ref={ref}>
      <button
        className="language-dropdown-trigger"
        onClick={() => setOpen(!open)}
        aria-label={t("switchLanguage")}
      >
        <span className="language-dropdown-flag">{current.flag}</span>
        <span className="language-dropdown-text">{current.label}</span>
        <svg
          className={`language-dropdown-chevron${open ? " language-dropdown-chevron-open" : ""}`}
          width="12"
          height="12"
          viewBox="0 0 12 12"
          fill="none"
        >
          <path
            d="M3 4.5L6 7.5L9 4.5"
            stroke="currentColor"
            strokeWidth="1.5"
            strokeLinecap="round"
            strokeLinejoin="round"
          />
        </svg>
      </button>
      {open && (
        <div className="language-dropdown-menu">
          {LANGUAGE_OPTIONS.map((opt) => (
            <button
              key={opt.locale}
              className={`language-dropdown-item${opt.locale === locale ? " language-dropdown-item-selected" : ""}`}
              onClick={() => {
                setLocale(opt.locale);
                setOpen(false);
              }}
            >
              <span className="language-dropdown-flag">{opt.flag}</span>
              <span>{opt.label}</span>
              {opt.locale === locale && (
                <svg
                  className="language-dropdown-check"
                  width="14"
                  height="14"
                  viewBox="0 0 14 14"
                  fill="none"
                >
                  <path
                    d="M3 7L6 10L11 4"
                    stroke="currentColor"
                    strokeWidth="2"
                    strokeLinecap="round"
                    strokeLinejoin="round"
                  />
                </svg>
              )}
            </button>
          ))}
        </div>
      )}
    </div>
  );
}

interface AppOverflowMenuProps {
  t: (key: keyof TranslationKeys) => string;
  hasUpdate: boolean;
  links: LinkItem[];
  onClose: () => void;
  onOpenVersionModal: () => void;
  themeMode: ThemeMode;
  setThemeMode: (mode: ThemeMode) => void;
  markdownMode: "off" | "agent" | "all";
  setMarkdownMode: (mode: "off" | "agent" | "all") => void;
  onOpenDiffs?: () => void;
  onOpenTerminal?: () => void;
  onArchiveConversation?: () => Promise<void>;
  onOpenAgentsMd?: () => void;
  browserNotifsEnabled?: boolean;
  browserNotificationsDenied?: boolean;
  onEnableBrowserNotifications?: () => Promise<void>;
  onDisableBrowserNotifications?: () => void;
  locale?: Locale;
  setLocale?: (locale: Locale) => void;
}

export default function AppOverflowMenu({
  t,
  hasUpdate,
  links,
  onClose,
  onOpenVersionModal,
  themeMode,
  setThemeMode,
  markdownMode,
  setMarkdownMode,
  onOpenDiffs,
  onOpenTerminal,
  onArchiveConversation,
  onOpenAgentsMd,
  browserNotifsEnabled,
  browserNotificationsDenied,
  onEnableBrowserNotifications,
  onDisableBrowserNotifications,
  locale,
  setLocale,
}: AppOverflowMenuProps) {
  return (
    <div className="overflow-menu">
      {onOpenDiffs && (
        <button
          onClick={() => {
            onClose();
            onOpenDiffs();
          }}
          className="overflow-menu-item"
        >
          <svg fill="none" stroke="currentColor" viewBox="0 0 24 24" className="chat-menu-icon">
            <path
              strokeLinecap="round"
              strokeLinejoin="round"
              strokeWidth={2}
              d="M9 12h6m-6 4h6m2 5H7a2 2 0 01-2-2V5a2 2 0 012-2h5.586a1 1 0 01.707.293l5.414 5.414a1 1 0 01.293.707V19a2 2 0 01-2 2z"
            />
          </svg>
          {t("diffs")}
        </button>
      )}

      {onOpenTerminal && (
        <button
          onClick={() => {
            onClose();
            onOpenTerminal();
          }}
          className="overflow-menu-item"
        >
          <svg fill="none" stroke="currentColor" viewBox="0 0 24 24" className="chat-menu-icon">
            <path
              strokeLinecap="round"
              strokeLinejoin="round"
              strokeWidth={2}
              d="M8 9l3 3-3 3m5 0h3M5 20h14a2 2 0 002-2V6a2 2 0 00-2-2H5a2 2 0 00-2 2v12a2 2 0 002 2z"
            />
          </svg>
          {t("terminal")}
        </button>
      )}

      {links.map((link, index) => (
        <button
          key={index}
          onClick={() => {
            onClose();
            window.open(link.url, "_blank");
          }}
          className="overflow-menu-item"
        >
          <svg fill="none" stroke="currentColor" viewBox="0 0 24 24" className="chat-menu-icon">
            <path
              strokeLinecap="round"
              strokeLinejoin="round"
              strokeWidth={2}
              d={
                link.icon_svg ||
                "M10 6H6a2 2 0 00-2 2v10a2 2 0 002 2h10a2 2 0 002-2v-4M14 4h6m0 0v6m0-6L10 14"
              }
            />
          </svg>
          {link.title}
        </button>
      ))}

      {onArchiveConversation && (
        <>
          <div className="overflow-menu-divider" />
          <button
            onClick={async () => {
              onClose();
              await onArchiveConversation();
            }}
            className="overflow-menu-item"
          >
            <svg fill="none" stroke="currentColor" viewBox="0 0 24 24" className="chat-menu-icon">
              <path
                strokeLinecap="round"
                strokeLinejoin="round"
                strokeWidth={2}
                d="M5 8h14M8 8V6a4 4 0 118 0v2m-9 0v10a2 2 0 002 2h6a2 2 0 002-2V8"
              />
            </svg>
            {t("archiveConversation")}
          </button>
        </>
      )}

      {onOpenAgentsMd && (
        <>
          <div className="overflow-menu-divider" />
          <button
            onClick={() => {
              onClose();
              onOpenAgentsMd();
            }}
            className="overflow-menu-item"
          >
            <svg fill="none" stroke="currentColor" viewBox="0 0 24 24" className="chat-menu-icon">
              <path
                strokeLinecap="round"
                strokeLinejoin="round"
                strokeWidth={2}
                d="M11 5H6a2 2 0 00-2 2v11a2 2 0 002 2h11a2 2 0 002-2v-5m-1.414-9.414a2 2 0 112.828 2.828L11.828 15H9v-2.828l8.586-8.586z"
              />
            </svg>
            {t("editUserAgentsMd")}
          </button>
        </>
      )}

      <div className="overflow-menu-divider" />
      <button
        onClick={() => {
          onClose();
          onOpenVersionModal();
        }}
        className="overflow-menu-item"
      >
        <svg fill="none" stroke="currentColor" viewBox="0 0 24 24" className="chat-menu-icon">
          <path
            strokeLinecap="round"
            strokeLinejoin="round"
            strokeWidth={2}
            d="M4 4v5h.582m15.356 2A8.001 8.001 0 004.582 9m0 0H9m11 11v-5h-.581m0 0a8.003 8.003 0 01-15.357-2m15.357 2H15"
          />
        </svg>
        {t("checkForNewVersion")}
        {hasUpdate && <span className="version-menu-dot" />}
      </button>

      <div className="overflow-menu-divider" />
      <div className="theme-toggle-row">
        <button
          onClick={() => {
            setThemeMode("system");
            setStoredTheme("system");
            applyTheme("system");
          }}
          className={`theme-toggle-btn${themeMode === "system" ? " theme-toggle-btn-selected" : ""}`}
          title={t("system")}
        >
          <svg fill="none" stroke="currentColor" viewBox="0 0 24 24">
            <path
              strokeLinecap="round"
              strokeLinejoin="round"
              strokeWidth={2}
              d="M9.75 17L9 20l-1 1h8l-1-1-.75-3M3 13h18M5 17h14a2 2 0 002-2V5a2 2 0 00-2-2H5a2 2 0 00-2 2v10a2 2 0 002 2z"
            />
          </svg>
        </button>
        <button
          onClick={() => {
            setThemeMode("light");
            setStoredTheme("light");
            applyTheme("light");
          }}
          className={`theme-toggle-btn${themeMode === "light" ? " theme-toggle-btn-selected" : ""}`}
          title={t("light")}
        >
          <svg fill="none" stroke="currentColor" viewBox="0 0 24 24">
            <path
              strokeLinecap="round"
              strokeLinejoin="round"
              strokeWidth={2}
              d="M12 3v1m0 16v1m9-9h-1M4 12H3m15.364 6.364l-.707-.707M6.343 6.343l-.707-.707m12.728 0l-.707.707M6.343 17.657l-.707.707M16 12a4 4 0 11-8 0 4 4 0 018 0z"
            />
          </svg>
        </button>
        <button
          onClick={() => {
            setThemeMode("dark");
            setStoredTheme("dark");
            applyTheme("dark");
          }}
          className={`theme-toggle-btn${themeMode === "dark" ? " theme-toggle-btn-selected" : ""}`}
          title={t("dark")}
        >
          <svg fill="none" stroke="currentColor" viewBox="0 0 24 24">
            <path
              strokeLinecap="round"
              strokeLinejoin="round"
              strokeWidth={2}
              d="M20.354 15.354A9 9 0 018.646 3.646 9.003 9.003 0 0012 21a9.003 9.003 0 008.354-5.646z"
            />
          </svg>
        </button>
      </div>

      {typeof browserNotifsEnabled === "boolean" &&
        onEnableBrowserNotifications &&
        onDisableBrowserNotifications && (
          <>
            <div className="overflow-menu-divider" />
            <div className="theme-toggle-row">
              <button
                onClick={async () => {
                  if (browserNotifsEnabled) return;
                  await onEnableBrowserNotifications();
                }}
                className={`theme-toggle-btn${browserNotifsEnabled ? " theme-toggle-btn-selected" : ""}`}
                title={browserNotificationsDenied ? t("blockedByBrowser") : t("enableNotifications")}
                disabled={browserNotificationsDenied}
              >
                <svg fill="none" stroke="currentColor" viewBox="0 0 24 24">
                  <path
                    strokeLinecap="round"
                    strokeLinejoin="round"
                    strokeWidth={2}
                    d="M15 17h5l-1.405-1.405A2.032 2.032 0 0118 14.158V11a6.002 6.002 0 00-4-5.659V5a2 2 0 10-4 0v.341C7.67 6.165 6 8.388 6 11v3.159c0 .538-.214 1.055-.595 1.436L4 17h5m6 0v1a3 3 0 11-6 0v-1m6 0H9"
                  />
                </svg>
              </button>
              <button
                onClick={() => {
                  if (!browserNotifsEnabled) return;
                  onDisableBrowserNotifications();
                }}
                className={`theme-toggle-btn${!browserNotifsEnabled ? " theme-toggle-btn-selected" : ""}`}
                title={t("disableNotifications")}
              >
                <svg fill="none" stroke="currentColor" viewBox="0 0 24 24">
                  <path
                    strokeLinecap="round"
                    strokeLinejoin="round"
                    strokeWidth={2}
                    d="M5.586 15H4l1.405-1.405A2.032 2.032 0 006 12.158V9a6.002 6.002 0 014-5.659V3a2 2 0 114 0v.341c.588.17 1.14.432 1.636.772M15 17h-6v1a3 3 0 006 0v-1zM18 9a3 3 0 00-3-3M3 3l18 18"
                  />
                </svg>
              </button>
            </div>
          </>
        )}

      <div className="overflow-menu-divider" />
      <div className="md-toggle-row">
        <div className="md-toggle-label">{t("markdown")}</div>
        <div className="md-toggle-buttons">
          <button
            onClick={() => setMarkdownMode("off")}
            className={`md-toggle-btn${markdownMode === "off" ? " md-toggle-btn-selected" : ""}`}
            title={t("showPlainText")}
          >
            {t("off")}
          </button>
          <button
            onClick={() => setMarkdownMode("agent")}
            className={`md-toggle-btn${markdownMode === "agent" ? " md-toggle-btn-selected" : ""}`}
            title={t("renderMarkdownAgent")}
          >
            {t("agent")}
          </button>
          <button
            onClick={() => setMarkdownMode("all")}
            className={`md-toggle-btn${markdownMode === "all" ? " md-toggle-btn-selected" : ""}`}
            title={t("renderMarkdownAll")}
          >
            {t("all")}
          </button>
        </div>
      </div>

      {locale && setLocale && (
        <>
          <div className="overflow-menu-divider" />
          <div className="language-selector-row">
            <div className="md-toggle-label">
              {t("language")}{" "}
              <a
                href={`https://github.com/boldsoftware/shelley/issues/new?labels=translation&title=${encodeURIComponent("Translation issue: ")}&body=${encodeURIComponent("**Language:** \n**Where in the UI:** \n**Current text:** \n**Suggested text:** \n")}`}
                target="_blank"
                rel="noopener noreferrer"
                className="report-bug-link"
                onClick={(e) => e.stopPropagation()}
              >
                [{t("reportBug")}]
              </a>
            </div>
            <LanguageDropdown locale={locale} setLocale={setLocale} t={t} />
          </div>
        </>
      )}
    </div>
  );
}
