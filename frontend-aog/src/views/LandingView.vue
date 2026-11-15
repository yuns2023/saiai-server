<script setup lang="ts">
import { computed } from 'vue'
import { useI18n } from 'vue-i18n'
import PrefsMenu from '@/components/PrefsMenu.vue'
import { useAppStore } from '@/stores/app'
import { useAuthStore } from '@/stores/auth'

const { t } = useI18n()
const app = useAppStore()
const auth = useAuthStore()
const canRegister = computed(() => app.settings?.registration_enabled !== false)
const docUrl = computed(() => app.settings?.doc_url?.trim() || '')
const contact = computed(() => app.settings?.contact_info?.trim() || '')

/** Illustrative statement line (labelled as an example; not real prices). */
const sample = [
  { label: 'charge.input', tokens: '18,240', unit: '$3.00', amount: '$0.0547' },
  { label: 'charge.cacheRead', tokens: '96,512', unit: '$0.30', amount: '$0.0290' },
  { label: 'charge.output', tokens: '1,906', unit: '$15.00', amount: '$0.0286' }
]
</script>

<template>
  <div class="flex min-h-dvh flex-col bg-paper">
    <header class="flex h-14 items-center justify-between px-5 sm:px-10">
      <span class="flex items-center gap-2">
        <img v-if="app.siteLogo" :src="app.siteLogo" alt="" class="h-6 w-6 rounded-sm object-contain" />
        <span class="text-md font-semibold tracking-tight">{{ app.siteName }}</span>
      </span>
      <PrefsMenu compact />
    </header>

    <main class="mx-auto grid w-full max-w-6xl flex-1 items-center gap-14 px-5 py-12 sm:px-10 lg:grid-cols-[1.1fr_1fr]">
      <div>
        <h1 class="max-w-[16ch] text-[clamp(2rem,4.6vw,3.25rem)] font-semibold leading-[1.15] tracking-[-0.02em]">
          {{ t('landing.headline') }}
        </h1>
        <p class="mt-5 max-w-[46ch] text-md leading-relaxed text-ink-2">{{ t('landing.sub') }}</p>
        <div class="mt-8 flex flex-wrap gap-3">
          <template v-if="auth.isAuthenticated">
            <RouterLink to="/dashboard" class="btn btn-primary h-11 px-6 text-base no-underline">{{ t('landing.enter') }}</RouterLink>
          </template>
          <template v-else>
            <RouterLink to="/login" class="btn btn-primary h-11 px-6 text-base no-underline">{{ t('auth.signIn') }}</RouterLink>
            <RouterLink v-if="canRegister" to="/register" class="btn btn-line h-11 px-6 text-base no-underline">{{ t('auth.register') }}</RouterLink>
          </template>
        </div>
      </div>

      <!-- A receipt that shows the mechanism: every charge itemised. -->
      <figure class="receipt w-full max-w-md justify-self-center border border-rule bg-paper px-6 pb-6 pt-5 lg:justify-self-end" :aria-label="t('landing.sampleLabel')">
        <figcaption class="flex items-baseline justify-between text-xs text-ink-3">
          <span>{{ t('landing.sampleTitle') }}</span>
          <span>{{ t('landing.sampleNote') }}</span>
        </figcaption>
        <div class="rule-double mt-2 pb-2 font-mono text-xs text-ink-2">claude-sonnet · 14:02:31</div>
        <dl class="mt-3 space-y-1.5 text-sm">
          <div v-for="row in sample" :key="row.label" class="flex items-baseline justify-between gap-4">
            <dt class="text-ink-2">{{ t(row.label) }}</dt>
            <dd class="num">
              <span class="text-ink-3">{{ row.tokens }} × {{ row.unit }}/M =</span> {{ row.amount }}
            </dd>
          </div>
          <div class="flex justify-between gap-4 border-t border-rule pt-1.5">
            <dt class="text-ink-2">{{ t('charge.standard') }}</dt>
            <dd class="num">$0.1123</dd>
          </div>
          <div class="flex justify-between gap-4">
            <dt class="text-ink-2">{{ t('charge.groupRate') }}</dt>
            <dd class="num">×0.8</dd>
          </div>
        </dl>
        <div class="rule-double mt-2" />
        <div class="mt-2 flex items-baseline justify-between">
          <span class="font-medium">{{ t('charge.billedBalance') }}</span>
          <span class="num text-lg font-semibold">$0.0898</span>
        </div>
      </figure>
    </main>

    <footer class="flex flex-wrap items-center gap-x-6 gap-y-2 border-t border-rule-soft px-5 py-5 text-xs text-ink-3 sm:px-10">
      <RouterLink to="/key-usage">{{ t('landing.lookup') }}</RouterLink>
      <a v-if="docUrl" :href="docUrl" target="_blank" rel="noopener noreferrer">{{ t('landing.docs') }}</a>
      <span v-if="contact">{{ t('landing.contact') }} {{ contact }}</span>
    </footer>
  </div>
</template>

<style scoped>
.receipt {
  box-shadow: 0 18px 40px -28px rgb(20 32 58 / 0.35);
}
</style>
