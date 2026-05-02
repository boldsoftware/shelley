import { createContext, useCallback, useContext, useMemo, useState } from "react";
import type { Locale, TranslationKeys } from "./types";
import { en } from "./en";
import { ja } from "./ja";
import { fr } from "./fr";
import { ru } from "./ru";
import { es } from "./es";
import { upgoer5 } from "./upgoer5";
import { zhCN } from "./zh-CN";
import { zhTW } from "./zh-TW";
import { vn } from "./vi";

const LOCALE_STORAGE_KEY = "shelley-locale";

const translations: Record<Locale, TranslationKeys> = {
  en,
  ja,
  fr,
  ru,
  es,
  "zh-CN": zhCN,
  "zh-TW": zhTW,
  upgoer5,
  vi: vn,
};

function getStoredLocale(): Locale {
  try {
    const stored = localStorage.getItem(LOCALE_STORAGE_KEY);
    if (stored && stored in translations) {
      return stored as Locale;
    }
  } catch {
    // localStorage may be unavailable
  }
  return "en";
}

interface I18nContextValue {
  locale: Locale;
  setLocale: (locale: Locale) => void;
  t: (key: keyof TranslationKeys) => string;
}

const I18nContext = createContext<I18nContextValue | null>(null);

export function I18nProvider({ children }: { children: React.ReactNode }) {
  const [locale, setLocaleState] = useState<Locale>(getStoredLocale);

  const setLocale = useCallback((newLocale: Locale) => {
    setLocaleState(newLocale);
    try {
      localStorage.setItem(LOCALE_STORAGE_KEY, newLocale);
    } catch {
      // localStorage may be unavailable
    }
  }, []);

  const t = useCallback(
    (key: keyof TranslationKeys): string => {
      const localeTranslations = translations[locale];
      const value = localeTranslations[key];
      if (value !== undefined && value !== "") {
        return value;
      }
      // Fall back to English
      return en[key];
    },
    [locale],
  );

  const value = useMemo(() => ({ locale, setLocale, t }), [locale, setLocale, t]);

  return <I18nContext.Provider value={value}>{children}</I18nContext.Provider>;
}

export function useI18n(): I18nContextValue {
  const context = useContext(I18nContext);
  if (!context) {
    throw new Error("useI18n must be used within an I18nProvider");
  }
  return context;
}

export { I18nContext };
