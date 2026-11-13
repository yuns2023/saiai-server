import { createI18n } from 'vue-i18n'
import zh from './locales/zh'
import en from './locales/en'

export type LocaleCode = 'zh' | 'en'

/** Same key as the classic console so the language choice carries over. */
const LOCALE_KEY = 'sub2api_locale'

function initialLocale(): LocaleCode {
  const saved = localStorage.getItem(LOCALE_KEY)
  if (saved === 'zh' || saved === 'en') return saved
  return navigator.language.toLowerCase().startsWith('zh') ? 'zh' : 'en'
}

export const i18n = createI18n({
  legacy: false,
  locale: initialLocale(),
  fallbackLocale: 'zh',
  messages: { zh, en }
})

export function getLocale(): LocaleCode {
  return i18n.global.locale.value as LocaleCode
}

/** BCP 47 tag for Intl formatting. */
export function intlLocale(): string {
  return getLocale() === 'zh' ? 'zh-CN' : 'en-US'
}

export function setLocale(code: LocaleCode): void {
  i18n.global.locale.value = code
  localStorage.setItem(LOCALE_KEY, code)
  document.documentElement.lang = code === 'zh' ? 'zh-CN' : 'en'
}
