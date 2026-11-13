<script setup lang="ts">
/** Language and theme switches, used in the rail and on public pages. */
import { computed } from 'vue'
import { useI18n } from 'vue-i18n'
import { setLocale, type LocaleCode } from '@/i18n'
import { useAppStore, type ThemePref } from '@/stores/app'
import Icon from './Icon.vue'

defineProps<{ compact?: boolean }>()
const { t, locale } = useI18n()
const app = useAppStore()

const nextLocale = computed<LocaleCode>(() => (locale.value === 'zh' ? 'en' : 'zh'))
const themeOrder: ThemePref[] = ['system', 'light', 'dark']
const themeIcon = computed(() => (app.theme === 'dark' ? 'moon' : app.theme === 'light' ? 'sun' : 'monitor'))

function cycleTheme(): void {
  const i = themeOrder.indexOf(app.theme)
  app.setTheme(themeOrder[(i + 1) % themeOrder.length])
}
</script>

<template>
  <div class="flex items-center gap-1">
    <button
      type="button"
      class="btn btn-quiet btn-sm text-ink-2"
      :title="t('prefs.language')"
      @click="setLocale(nextLocale)"
    >
      <Icon name="globe" :size="15" />
      <span>{{ locale === 'zh' ? 'English' : '中文' }}</span>
    </button>
    <button
      type="button"
      class="btn btn-quiet btn-sm text-ink-2"
      :title="t('prefs.theme')"
      :aria-label="t('prefs.themeNow', { mode: t(`prefs.theme_${app.theme}`) })"
      @click="cycleTheme"
    >
      <Icon :name="themeIcon" :size="15" />
      <span v-if="!compact">{{ t(`prefs.theme_${app.theme}`) }}</span>
    </button>
  </div>
</template>
