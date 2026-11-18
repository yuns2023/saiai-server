<script setup lang="ts">
/**
 * 托管我的 Claude 号: generate a setup-token authorisation link, paste the
 * `code#state` claude.ai shows, name the account, optionally give a proxy and
 * submit it for review. The code is never kept: the field is cleared after
 * every submission and whenever its link expires or is replaced. A failure
 * that used up the link asks for a new one and keeps the name and the proxy
 * address (not its password).
 */
import { computed, onUnmounted, reactive, ref, watch } from 'vue'
import { useI18n } from 'vue-i18n'
import { hostedApi, type HostedApplication, type SelfOnboardAuthUrl, type SelfOnboardConfig } from '@/api'
import {
  checkDisplayName,
  checkProxy,
  emptyProxy,
  MAX_DISPLAY_NAME,
  normalizeAuthCode,
  onboardErrorKey,
  PROXY_PROTOCOLS,
  proxyPayload,
  submitNeedsNewLink,
  type ProxyField
} from '@/lib/selfOnboard'

const props = defineProps<{ config: SelfOnboardConfig }>()
const emit = defineEmits<{ submitted: [app: HostedApplication] }>()
const { t } = useI18n()

const proxyRequired = computed(() => !props.config.exchange_proxy_configured)

/* ---------- Step 1: authorisation link ---------- */
const link = ref<SelfOnboardAuthUrl | null>(null)
const linking = ref(false)
const now = ref(Date.now())
let ticker: number | undefined
let alive = true

const expiresMs = computed(() => (link.value ? Date.parse(link.value.expires_at) : NaN))
const remainingMs = computed(() => (Number.isFinite(expiresMs.value) ? Math.max(0, expiresMs.value - now.value) : 0))
const linkLive = computed(() => !!link.value && remainingMs.value > 0)
const countdown = computed(() => {
  const s = Math.ceil(remainingMs.value / 1000)
  return `${Math.floor(s / 60)}:${String(s % 60).padStart(2, '0')}`
})

function startTicker(): void {
  stopTicker()
  now.value = Date.now()
  ticker = window.setInterval(() => {
    now.value = Date.now()
    if (!linkLive.value) stopTicker()
  }, 1000)
}

function stopTicker(): void {
  if (ticker !== undefined) window.clearInterval(ticker)
  ticker = undefined
}

function dropLink(): void {
  link.value = null
  stopTicker()
}

async function generate(): Promise<void> {
  if (linking.value || submitting.value) return
  linking.value = true
  error.value = ''
  newLinkNeeded.value = false
  done.value = false
  // The old link is replaced either way; its code must not be reused.
  dropLink()
  code.value = ''
  try {
    const fresh = await hostedApi.createAuthUrl()
    if (!alive) return
    link.value = fresh
    startTicker()
  } catch (e) {
    if (alive) error.value = t(`onboard.errors.${onboardErrorKey(e)}`, { n: props.config.max_pending })
  } finally {
    linking.value = false
  }
}

/* ---------- Steps 2–3: code, name, proxy ---------- */
const code = ref('')
const displayName = ref('')
const proxy = reactive(emptyProxy())
/** Field problems are shown once the user tried to submit. */
const tried = ref(false)

// An expired link takes its pasted code with it.
watch(linkLive, (live) => {
  if (!live) code.value = ''
})

const codeProblem = computed(() => (normalizeAuthCode(code.value) ? '' : 'codeInvalid'))
const nameProblem = computed(() => checkDisplayName(displayName.value))
const proxyProblems = computed(() => checkProxy(proxy, proxyRequired.value))
const hasProblems = computed(() => !!codeProblem.value || !!nameProblem.value || Object.keys(proxyProblems.value).length > 0)

function proxyMsg(field: ProxyField): string {
  const key = proxyProblems.value[field]
  return tried.value && key ? t(`onboard.check.${key}`) : ''
}

/* ---------- Step 4: submit ---------- */
const submitting = ref(false)
const error = ref('')
const newLinkNeeded = ref(false)
const done = ref(false)

async function submit(): Promise<void> {
  if (submitting.value || linking.value) return
  tried.value = true
  done.value = false
  if (!linkLive.value || hasProblems.value || !link.value) return
  const body = {
    session_id: link.value.session_id,
    code: normalizeAuthCode(code.value),
    display_name: displayName.value.trim(),
    proxy: proxyPayload(proxy)
  }
  // The code is single use and secret: never keep it, whatever happens next.
  code.value = ''
  submitting.value = true
  error.value = ''
  newLinkNeeded.value = false
  try {
    const app = await hostedApi.submitApplication(body)
    if (!alive) return
    dropLink()
    displayName.value = ''
    Object.assign(proxy, emptyProxy())
    tried.value = false
    done.value = true
    emit('submitted', app)
  } catch (e) {
    if (!alive) return
    error.value = t(`onboard.errors.${onboardErrorKey(e)}`, { n: props.config.max_pending })
    if (submitNeedsNewLink(e)) {
      dropLink()
      proxy.password = ''
      newLinkNeeded.value = true
    }
  } finally {
    submitting.value = false
    // Anything pasted while the request was out belongs to a link that is now used or gone.
    code.value = ''
  }
}

onUnmounted(() => {
  alive = false
  stopTicker()
})
</script>

<template>
  <section class="mt-12" aria-labelledby="onboard-title" data-testid="onboard">
    <h2 id="onboard-title" class="text-md font-semibold">{{ t('onboard.title') }}</h2>
    <p class="mt-1 max-w-[70ch] text-sm text-ink-2">{{ t('onboard.lead') }}</p>
    <p class="mt-1 max-w-[70ch] text-sm text-ink-2" data-testid="onboard-fee">{{ t('onboard.fee') }}</p>

    <form class="mt-4 border-t border-rule-strong" novalidate @submit.prevent="submit">
      <ol>
        <!-- 1. Link -->
        <li class="grid gap-x-6 gap-y-2 border-b border-rule-soft py-5 md:grid-cols-[12rem_minmax(0,1fr)]">
          <h3 class="text-sm font-semibold"><span class="num text-ink-3">1</span> · {{ t('onboard.step1') }}</h3>
          <div class="min-w-0">
            <p class="text-sm text-ink-2">{{ t('onboard.step1Lead') }}</p>
            <div class="mt-3 flex flex-wrap items-center gap-x-4 gap-y-2">
              <button type="button" class="btn" :class="link ? 'btn-line' : 'btn-primary'" :disabled="linking || submitting" data-testid="onboard-generate" @click="generate">
                {{ linking ? t('onboard.generating') : link ? t('onboard.regenerate') : t('onboard.generate') }}
              </button>
              <template v-if="link && linkLive">
                <a :href="link.auth_url" target="_blank" rel="noopener noreferrer" class="btn btn-primary" data-testid="onboard-link">{{ t('onboard.openLink') }}</a>
                <span class="num text-sm text-ink-2" data-testid="onboard-countdown">{{ t('onboard.expiresIn', { time: countdown }) }}</span>
              </template>
              <span v-else-if="link" class="text-sm text-seal" role="alert" data-testid="onboard-expired">{{ t('onboard.linkExpired') }}</span>
            </div>
          </div>
        </li>

        <!-- 2. Code -->
        <li class="grid gap-x-6 gap-y-2 border-b border-rule-soft py-5 md:grid-cols-[12rem_minmax(0,1fr)]">
          <h3 class="text-sm font-semibold"><span class="num text-ink-3">2</span> · {{ t('onboard.step2') }}</h3>
          <div class="min-w-0">
            <p class="text-sm text-ink-2">{{ t('onboard.step2Lead') }}</p>
            <label class="mt-3 block max-w-[36rem]">
              <span class="label">{{ t('onboard.code') }}</span>
              <input
                v-model="code"
                type="text"
                class="field mt-1 font-mono text-xs"
                autocomplete="off"
                autocapitalize="off"
                spellcheck="false"
                :disabled="!linkLive || submitting"
                :placeholder="linkLive ? t('onboard.codePlaceholder') : t('onboard.codeNeedsLink')"
                :aria-invalid="tried && linkLive && codeProblem ? 'true' : undefined"
                data-testid="onboard-code"
              />
            </label>
            <p v-if="tried && linkLive && codeProblem" class="mt-1 text-xs text-seal">{{ t(`onboard.check.${codeProblem}`) }}</p>
          </div>
        </li>

        <!-- 3. Name and proxy -->
        <li class="grid gap-x-6 gap-y-2 border-b border-rule-soft py-5 md:grid-cols-[12rem_minmax(0,1fr)]">
          <h3 class="text-sm font-semibold"><span class="num text-ink-3">3</span> · {{ t('onboard.step3') }}</h3>
          <div class="min-w-0 max-w-[36rem]">
            <label class="block">
              <span class="label">{{ t('onboard.displayName') }}</span>
              <input
                v-model="displayName"
                type="text"
                class="field mt-1"
                :maxlength="MAX_DISPLAY_NAME * 2"
                :placeholder="t('onboard.displayNamePlaceholder')"
                :aria-invalid="tried && nameProblem ? 'true' : undefined"
                data-testid="onboard-name"
              />
            </label>
            <p class="mt-1 text-xs" :class="tried && nameProblem ? 'text-seal' : 'text-ink-3'">
              {{ tried && nameProblem ? t(`onboard.check.${nameProblem}`) : t('onboard.displayNameHint') }}
            </p>

            <fieldset class="mt-6" data-testid="onboard-proxy">
              <legend class="text-sm font-semibold">{{ proxyRequired ? t('onboard.proxyTitleRequired') : t('onboard.proxyTitle') }}</legend>
              <p class="mt-1 text-sm text-ink-2" data-testid="onboard-proxy-why">{{ proxyRequired ? t('onboard.proxyWhyRequired') : t('onboard.proxyWhy') }}</p>
              <div class="mt-3 grid grid-cols-[8rem_minmax(0,1fr)] gap-x-3 gap-y-3 sm:grid-cols-[8rem_minmax(0,1fr)_6.5rem]">
                <label class="block">
                  <span class="label">{{ t('onboard.protocol') }}</span>
                  <select v-model="proxy.protocol" class="field mt-1" data-testid="onboard-proxy-protocol">
                    <option v-for="p in PROXY_PROTOCOLS" :key="p" :value="p">{{ p }}</option>
                  </select>
                </label>
                <label class="block">
                  <span class="label">{{ t('onboard.host') }}</span>
                  <input
                    v-model="proxy.host"
                    type="text"
                    class="field mt-1"
                    autocomplete="off"
                    autocapitalize="off"
                    spellcheck="false"
                    :placeholder="t('onboard.hostPlaceholder')"
                    :aria-invalid="proxyMsg('host') ? 'true' : undefined"
                    data-testid="onboard-proxy-host"
                  />
                </label>
                <label class="col-span-2 block sm:col-span-1">
                  <span class="label">{{ t('onboard.port') }}</span>
                  <input
                    v-model="proxy.port"
                    type="text"
                    inputmode="numeric"
                    class="field num mt-1"
                    autocomplete="off"
                    placeholder="1080"
                    :aria-invalid="proxyMsg('port') ? 'true' : undefined"
                    data-testid="onboard-proxy-port"
                  />
                </label>
              </div>
              <p v-if="proxyMsg('host') || proxyMsg('port') || proxyMsg('protocol')" class="mt-1 text-xs text-seal" data-testid="onboard-proxy-error">
                {{ proxyMsg('host') || proxyMsg('port') || proxyMsg('protocol') }}
              </p>
              <div class="mt-3 grid gap-3 sm:grid-cols-2">
                <label class="block">
                  <span class="label">{{ t('onboard.username') }}</span>
                  <input
                    v-model="proxy.username"
                    type="text"
                    class="field mt-1"
                    autocomplete="off"
                    autocapitalize="off"
                    spellcheck="false"
                    :aria-invalid="proxyMsg('auth') || proxyMsg('username') ? 'true' : undefined"
                    data-testid="onboard-proxy-username"
                  />
                </label>
                <label class="block">
                  <span class="label">{{ t('onboard.password') }}</span>
                  <input
                    v-model="proxy.password"
                    type="password"
                    class="field mt-1"
                    autocomplete="new-password"
                    :aria-invalid="proxyMsg('auth') || proxyMsg('password') ? 'true' : undefined"
                    data-testid="onboard-proxy-password"
                  />
                </label>
              </div>
              <p class="mt-1 text-xs" :class="proxyMsg('auth') || proxyMsg('username') || proxyMsg('password') ? 'text-seal' : 'text-ink-3'" data-testid="onboard-auth-hint">
                {{ proxyMsg('auth') || proxyMsg('username') || proxyMsg('password') || t('onboard.authHint') }}
              </p>
            </fieldset>
          </div>
        </li>

        <!-- 4. Submit -->
        <li class="grid gap-x-6 gap-y-2 py-5 md:grid-cols-[12rem_minmax(0,1fr)]">
          <h3 class="text-sm font-semibold"><span class="num text-ink-3">4</span> · {{ t('onboard.step4') }}</h3>
          <div class="min-w-0">
            <p class="text-sm text-ink-2">{{ t('onboard.submitLead') }}</p>
            <div class="mt-3 flex flex-wrap items-center gap-x-4 gap-y-2">
              <button type="submit" class="btn btn-primary" :disabled="submitting || !linkLive" data-testid="onboard-submit">
                {{ submitting ? t('onboard.submitting') : t('onboard.submit') }}
              </button>
              <span v-if="!linkLive && !error" class="text-xs text-ink-3">{{ t('onboard.codeNeedsLink') }}</span>
            </div>
            <p v-if="error" class="mt-3 text-sm text-seal" role="alert" data-testid="onboard-error">
              {{ error }}<template v-if="newLinkNeeded"> {{ t('onboard.newLinkNeeded') }}</template>
            </p>
            <p v-else-if="done" class="mt-3 text-sm text-ok" role="status" data-testid="onboard-done">{{ t('onboard.submitted') }}</p>
          </div>
        </li>
      </ol>
    </form>
  </section>
</template>
