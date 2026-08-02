import { createI18n } from "vue-i18n";
import zh from "./zh";
import en from "./en";

export type SupportedLocale = "zh" | "en";

const LOCALE_STORAGE_KEY = "nucleagent_locale";

function detectLocale(): SupportedLocale {
  const saved = localStorage.getItem(LOCALE_STORAGE_KEY);
  if (saved === "zh" || saved === "en") return saved;
  const browser = navigator.language.toLowerCase();
  return browser.startsWith("en") ? "en" : "zh";
}

const initialLocale = detectLocale();
document.documentElement.lang = initialLocale;

const i18n = createI18n({
  legacy: false,
  locale: initialLocale,
  fallbackLocale: "en",
  messages: {
    zh,
    en,
  },
});

export function setLocale(locale: SupportedLocale): void {
  i18n.global.locale.value = locale;
  localStorage.setItem(LOCALE_STORAGE_KEY, locale);
  document.documentElement.lang = locale;
}

export function getLocale(): SupportedLocale {
  return i18n.global.locale.value as SupportedLocale;
}

export default i18n;
