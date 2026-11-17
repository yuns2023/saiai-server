<script setup lang="ts">
import { computed } from 'vue'
import { useI18n } from 'vue-i18n'
import PrefsMenu from '@/components/PrefsMenu.vue'
import InviteHint from '@/components/InviteHint.vue'
import { useAppStore } from '@/stores/app'
import { useAuthStore } from '@/stores/auth'

const AITP_URL = 'https://github.com/2742zdz/AITP'
/** Product name kept on one line wherever it appears in the headline. */
const KEEP_TOGETHER = 'Claude Code'

const { t } = useI18n()
const app = useAppStore()
const auth = useAuthStore()
const canRegister = computed(() => app.settings?.registration_enabled !== false)
const inviteOnly = computed(() => canRegister.value && app.settings?.invitation_code_enabled === true)
/** Admin-set docs link: only http(s) URLs or site-relative paths become a link. */
const docUrl = computed(() => {
  const raw = app.settings?.doc_url?.trim() || ''
  if (/^\/(?![/\\])/.test(raw)) return raw
  try {
    const u = new URL(raw)
    return u.protocol === 'http:' || u.protocol === 'https:' ? u.href : ''
  } catch {
    return ''
  }
})
const contact = computed(() => app.settings?.contact_info?.trim() || '')
const year = new Date().getFullYear()

/** Headline split so "Claude Code" never breaks across lines. */
const headline = computed(() => {
  const text = app.siteSubtitle || t('landing.headline')
  return text.split(KEEP_TOGETHER).flatMap((part, i) => (i === 0 ? [{ text: part, keep: false }] : [{ text: KEEP_TOGETHER, keep: true }, { text: part, keep: false }]))
})

/** Step 1 follows the public registration settings. */
const steps = computed(() => {
  const first = !canRegister.value ? 'landing.step1Closed' : inviteOnly.value ? 'landing.step1' : 'landing.step1Open'
  return [
    { title: `${first}Title`, body: `${first}Body` },
    { title: 'landing.step2Title', body: 'landing.step2Body' },
    { title: 'landing.step3Title', body: 'landing.step3Body' }
  ]
})

/**
 * Ways to add balance, one statement line each: the method, an optional note
 * under it, and when it lands. Only methods live on the site; a new method is
 * one more entry here plus its copy.
 */
const payMethods: Array<{ title: string; body?: string; time: string }> = [
  { title: 'landing.paySolanaTitle', time: 'landing.paySolanaTime' },
  { title: 'landing.payArbitrumTitle', time: 'landing.payArbitrumTime' },
  { title: 'landing.payRedeemTitle', body: 'landing.payRedeemBody', time: 'landing.payRedeemTime' }
]

/** Hosting as ruled key-value lines; the last one (how to start) carries the site contact. */
const hostedRows = [
  { label: 'landing.hostedSeeLabel', value: 'landing.hostedSee' },
  { label: 'landing.hostedFeeLabel', value: 'landing.hostedFee' },
  { label: 'landing.hostedHowLabel', value: 'landing.hostedHow', contact: true }
]

const billing = ['landing.bill1', 'landing.bill2', 'landing.bill3']

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
      <div class="flex items-center gap-1">
        <PrefsMenu compact />
        <RouterLink v-if="!auth.isAuthenticated" to="/login" class="btn btn-line btn-sm ml-1 no-underline">{{ t('auth.signIn') }}</RouterLink>
      </div>
    </header>

    <main class="mx-auto w-full max-w-6xl flex-1 px-5 sm:px-10">
      <!-- What it is + how to connect: both answered in the first viewport. -->
      <section class="grid items-start gap-x-16 gap-y-10 pb-12 pt-10 sm:pt-20 lg:grid-cols-[1.05fr_1fr] lg:pb-20">
        <div>
          <h1 class="headline text-[clamp(2.125rem,4.4vw,3.25rem)] font-semibold leading-[1.15] tracking-[-0.02em]">
            <template v-for="(seg, i) in headline" :key="i">
              <span v-if="seg.keep" class="whitespace-nowrap">{{ seg.text }}</span>
              <template v-else>{{ seg.text }}</template>
            </template>
          </h1>
          <p class="mt-5 max-w-[34rem] text-md leading-relaxed text-ink-2">{{ t('landing.sub') }}</p>
          <div class="mt-8 flex flex-wrap gap-3">
            <RouterLink v-if="auth.isAuthenticated" to="/dashboard" class="btn btn-primary h-11 px-6 text-base no-underline">{{ t('landing.enter') }}</RouterLink>
            <template v-else>
              <RouterLink v-if="canRegister" to="/register" class="btn btn-primary h-11 px-6 text-base no-underline">{{ t('auth.register') }}</RouterLink>
              <RouterLink to="/login" class="btn h-11 px-6 text-base no-underline" :class="canRegister ? 'btn-line' : 'btn-primary'">{{ t('auth.signIn') }}</RouterLink>
            </template>
          </div>
          <div v-if="inviteOnly && !auth.isAuthenticated" class="mt-4 max-w-[34rem] text-sm text-ink-3">
            <p>{{ t('landing.inviteOnly') }}</p>
            <InviteHint />
          </div>
        </div>

        <section aria-labelledby="connect-title" class="lg:pt-2">
          <h2 id="connect-title" class="rule-double pb-2.5 text-lg font-semibold">{{ t('landing.connectTitle') }}</h2>
          <ol class="mt-1">
            <li v-for="(step, i) in steps" :key="step.title" class="grid grid-cols-[1.75rem_1fr] gap-x-2 border-b border-rule-soft py-4">
              <span class="num pt-px text-sm text-ink-3" aria-hidden="true">{{ i + 1 }}</span>
              <div>
                <h3 class="text-base font-semibold">{{ t(step.title) }}</h3>
                <p class="mt-1 text-sm leading-relaxed text-ink-2">
                  {{ t(step.body) }}
                  <a v-if="i === 2" :href="AITP_URL" target="_blank" rel="noopener noreferrer" class="whitespace-nowrap">{{ t('landing.aitpLink') }}</a>
                </p>
              </div>
            </li>
          </ol>
          <div class="rule-double" />
          <div class="mt-3 flex flex-wrap items-center gap-x-3 gap-y-2">
            <span class="text-sm text-ink-2">{{ t('landing.thenRun') }}</span>
            <code class="rounded-[4px] bg-shell px-2.5 py-1 font-mono text-sm text-ink">claude</code>
          </div>
        </section>
      </section>

      <!-- After connecting: how balance gets in (a small statement) and hosting your own account (ruled key-value lines). -->
      <section class="grid items-start gap-x-16 gap-y-12 border-t border-rule py-12 lg:grid-cols-[1.05fr_1fr] lg:py-16">
        <section aria-labelledby="pay-title" class="min-w-0" data-testid="landing-pay">
          <h2 id="pay-title" class="rule-double pb-2.5 text-lg font-semibold">{{ t('landing.payTitle') }}</h2>
          <table class="ledger mt-1 text-sm">
            <thead>
              <tr>
                <th scope="col">{{ t('landing.payColMethod') }}</th>
                <th scope="col" class="amount">{{ t('landing.payColTime') }}</th>
              </tr>
            </thead>
            <tbody>
              <tr v-for="m in payMethods" :key="m.title">
                <td>
                  <span class="font-medium text-ink">{{ t(m.title) }}</span>
                  <span v-if="m.body" class="mt-0.5 block text-ink-2">{{ t(m.body) }}</span>
                </td>
                <td class="amount">{{ t(m.time) }}</td>
              </tr>
            </tbody>
            <!-- One cell, so the long conversion never widens the time column. -->
            <tfoot>
              <tr>
                <td colspan="2">
                  <div class="flex flex-wrap items-baseline justify-between gap-x-4">
                    <span class="text-ink-2">{{ t('landing.payRateLabel') }}</span>
                    <span class="num font-medium">{{ t('landing.payRate') }}</span>
                  </div>
                </td>
              </tr>
            </tfoot>
          </table>
          <p class="mt-3 text-sm leading-relaxed text-ink-2">{{ t('landing.payNote') }}</p>
          <RouterLink v-if="auth.isAuthenticated" to="/billing" class="mt-2 inline-block text-sm">{{ t('landing.payGo') }}</RouterLink>
        </section>

        <section aria-labelledby="hosted-title" class="min-w-0" data-testid="landing-hosted">
          <h2 id="hosted-title" class="rule-double pb-2.5 text-lg font-semibold">{{ t('landing.hostedTitle') }}</h2>
          <p class="border-b border-rule-soft py-3.5 text-sm leading-relaxed text-ink">{{ t('landing.hostedSub') }}</p>
          <dl class="grid grid-cols-[max-content_1fr] text-sm">
            <template v-for="row in hostedRows" :key="row.label">
              <dt class="label border-b border-rule-soft py-3 pr-5 leading-relaxed">{{ t(row.label) }}</dt>
              <dd class="min-w-0 border-b border-rule-soft py-3 leading-relaxed text-ink-2">
                {{ t(row.value) }}
                <span v-if="row.contact && contact" class="block text-ink [overflow-wrap:anywhere]">{{ t('landing.contact') }} {{ contact }}</span>
              </dd>
            </template>
          </dl>
        </section>
      </section>

      <!-- Transparent billing: the sample receipt is the page's one lifted sheet. -->
      <section aria-labelledby="billing-title" class="grid items-start gap-x-16 gap-y-10 border-t border-rule py-12 lg:grid-cols-[1.05fr_1fr] lg:py-16">
        <div class="lg:pt-5">
          <h2 id="billing-title" class="text-xl font-semibold tracking-tight">{{ t('landing.billingTitle') }}</h2>
          <p class="mt-3 max-w-[34rem] text-base leading-relaxed text-ink-2">{{ t('landing.billingSub') }}</p>
          <ul class="mt-6 max-w-[34rem] border-t border-rule-soft">
            <li v-for="key in billing" :key="key" class="border-b border-rule-soft py-3 text-sm leading-relaxed text-ink-2">{{ t(key) }}</li>
          </ul>
        </div>

        <figure class="receipt w-full max-w-md justify-self-center border border-rule bg-paper px-6 pb-6 pt-5 lg:justify-self-end" :aria-label="t('landing.sampleLabel')">
          <figcaption class="flex items-baseline justify-between text-xs text-ink-3">
            <span>{{ t('landing.sampleTitle') }}</span>
            <span>{{ t('landing.sampleNote') }}</span>
          </figcaption>
          <div class="rule-double mt-2 pb-2 font-mono text-xs text-ink-2">claude-sonnet · 14:02:31</div>
          <dl class="mt-3 space-y-1.5 text-sm">
            <div v-for="row in sample" :key="row.label" class="flex items-baseline justify-between gap-4">
              <dt class="text-ink-2">{{ t(row.label) }}</dt>
              <dd class="num text-right">
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
      </section>
    </main>

    <footer class="border-t border-rule-soft">
      <div class="mx-auto flex w-full max-w-6xl flex-col gap-3 px-5 py-6 text-xs text-ink-3 sm:flex-row sm:items-center sm:justify-between sm:px-10">
        <nav class="flex flex-wrap gap-x-6 gap-y-2" :aria-label="t('landing.footerNav')">
          <RouterLink to="/key-usage">{{ t('landing.lookup') }}</RouterLink>
          <a :href="AITP_URL" target="_blank" rel="noopener noreferrer">{{ t('landing.aitpGithub') }}</a>
          <a v-if="docUrl" :href="docUrl" target="_blank" rel="noopener noreferrer">{{ t('landing.docs') }}</a>
          <span v-if="contact" class="min-w-0 [overflow-wrap:anywhere]">{{ t('landing.contact') }} {{ contact }}</span>
        </nav>
        <p class="num">© {{ year }} {{ app.siteName }}</p>
      </div>
    </footer>
  </div>
</template>

<style scoped>
/* CJK breaks only at spaces, so "专属入口" never splits mid-phrase. */
.headline {
  word-break: keep-all;
  overflow-wrap: anywhere;
}
p,
li {
  text-wrap: pretty;
}
.receipt {
  box-shadow: 0 18px 40px -28px rgb(20 32 58 / 0.35);
}
:global(.dark) .receipt {
  box-shadow: 0 18px 40px -24px rgb(11 18 32 / 0.9);
}
</style>
