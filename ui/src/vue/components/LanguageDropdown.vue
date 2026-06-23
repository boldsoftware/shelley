<!-- Vue port of the LanguageDropdown inner component from ChatInterface.tsx.
     Exports LANGUAGE_OPTIONS for parity. Preserves the language-dropdown-*
     class contract and the switchLanguage aria-label. -->
<template>
  <div class="language-dropdown" ref="rootRef">
    <button
      class="language-dropdown-trigger"
      :aria-label="t('switchLanguage')"
      @click="open = !open"
    >
      <span class="language-dropdown-flag">{{ current.flag }}</span>
      <span class="language-dropdown-text">{{ current.label }}</span>
      <svg
        :class="`language-dropdown-chevron${open ? ' language-dropdown-chevron-open' : ''}`"
        width="12"
        height="12"
        viewBox="0 0 12 12"
        fill="none"
      >
        <path
          d="M3 4.5L6 7.5L9 4.5"
          stroke="currentColor"
          stroke-width="1.5"
          stroke-linecap="round"
          stroke-linejoin="round"
        />
      </svg>
    </button>
    <div v-if="open" class="language-dropdown-menu">
      <button
        v-for="opt in LANGUAGE_OPTIONS"
        :key="opt.locale"
        :class="`language-dropdown-item${opt.locale === locale ? ' language-dropdown-item-selected' : ''}`"
        @click="selectLocale(opt.locale)"
      >
        <span class="language-dropdown-flag">{{ opt.flag }}</span>
        <span>{{ opt.label }}</span>
        <svg
          v-if="opt.locale === locale"
          class="language-dropdown-check"
          width="14"
          height="14"
          viewBox="0 0 14 14"
          fill="none"
        >
          <path
            d="M3 7L6 10L11 4"
            stroke="currentColor"
            stroke-width="2"
            stroke-linecap="round"
            stroke-linejoin="round"
          />
        </svg>
      </button>
    </div>
  </div>
</template>

<script setup lang="ts">
import { computed, onUnmounted, ref, watch } from "vue";
import type { Locale } from "../../i18n/types";
import { useI18n } from "../composables/i18n";

interface LanguageOption {
  locale: Locale;
  flag: string;
  label: string;
}

// LANGUAGE_OPTIONS mirrors the React module-level constant; kept local since
// nothing imports it externally (and <script setup> cannot re-export).
const LANGUAGE_OPTIONS: LanguageOption[] = [
  { locale: "en", flag: "\uD83C\uDDFA\uD83C\uDDF8", label: "English" },
  { locale: "ja", flag: "\uD83C\uDDEF\uD83C\uDDF5", label: "\u65E5\u672C\u8A9E" },
  { locale: "fr", flag: "\uD83C\uDDEB\uD83C\uDDF7", label: "Fran\u00E7ais" },
  {
    locale: "ru",
    flag: "\uD83C\uDDF7\uD83C\uDDFA",
    label: "\u0420\u0443\u0441\u0441\u043A\u0438\u0439",
  },
  { locale: "es", flag: "\uD83C\uDDEA\uD83C\uDDF8", label: "Espa\u00F1ol" },
  { locale: "zh-CN", flag: "\uD83C\uDDE8\uD83C\uDDF3", label: "\u7B80\u4F53\u4E2D\u6587" },
  { locale: "zh-TW", flag: "\uD83C\uDDF9\uD83C\uDDFC", label: "\u7E41\u9AD4\u4E2D\u6587" },
  { locale: "vi", flag: "\uD83C\uDDFB\uD83C\uDDF3", label: "Ti\u1EBFng Vi\u1EC7t" },
  { locale: "upgoer5", flag: "\uD83D\uDE80", label: "Up-Goer Five" },
];

const { t, locale, setLocale } = useI18n();
const open = ref(false);
const rootRef = ref<HTMLDivElement | null>(null);

const current = computed(
  () => LANGUAGE_OPTIONS.find((o) => o.locale === locale.value) || LANGUAGE_OPTIONS[0],
);

function selectLocale(l: Locale) {
  setLocale(l);
  open.value = false;
}

function handler(e: MouseEvent) {
  if (rootRef.value && !rootRef.value.contains(e.target as Node)) {
    open.value = false;
  }
}

watch(open, (isOpen) => {
  document.removeEventListener("mousedown", handler);
  if (isOpen) document.addEventListener("mousedown", handler);
});

onUnmounted(() => document.removeEventListener("mousedown", handler));
</script>
