// Vue i18n: reuses the existing locale dictionaries from src/i18n/*.
// Provides a reactive `locale` and a `t()` translator via Vue's provide/inject.
import { ref, inject, type App, type InjectionKey, type Ref } from "vue";
import type { Locale, TranslationKeys } from "../../i18n/types";
import { en } from "../../i18n/en";
import { ja } from "../../i18n/ja";
import { fr } from "../../i18n/fr";
import { ru } from "../../i18n/ru";
import { es } from "../../i18n/es";
import { upgoer5 } from "../../i18n/upgoer5";
import { zhCN } from "../../i18n/zh-CN";
import { zhTW } from "../../i18n/zh-TW";
import { vi } from "../../i18n/vi";

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
  vi,
};

export interface I18n {
  locale: Ref<Locale>;
  setLocale: (l: Locale) => void;
  t: (key: keyof TranslationKeys) => string;
}

export const I18nKey: InjectionKey<I18n> = Symbol("shelley-i18n");

function getStoredLocale(): Locale {
  try {
    const stored = localStorage.getItem(LOCALE_STORAGE_KEY);
    if (stored && stored in translations) return stored as Locale;
  } catch {
    // localStorage may be unavailable
  }
  return "en";
}

export function createI18n(): I18n {
  const locale = ref<Locale>(getStoredLocale());
  const setLocale = (l: Locale) => {
    locale.value = l;
    try {
      localStorage.setItem(LOCALE_STORAGE_KEY, l);
    } catch {
      // ignore
    }
  };
  const t = (key: keyof TranslationKeys): string => {
    const dict = translations[locale.value];
    const value = dict[key];
    if (value !== undefined && value !== "") return value;
    return en[key];
  };
  return { locale, setLocale, t };
}

export const i18nPlugin = {
  install(app: App) {
    const i18n = createI18n();
    app.provide(I18nKey, i18n);
    // Expose $t for templates as a convenience.
    app.config.globalProperties.$t = i18n.t;
  },
};

export function useI18n(): I18n {
  const i18n = inject(I18nKey);
  if (!i18n) throw new Error("useI18n must be used within app that installed i18nPlugin");
  return i18n;
}

export type { Locale, TranslationKeys };
