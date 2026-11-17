<script setup lang="ts">
/** How to point Claude Code at this key: one AITP command, with the raw values tucked away. */
import { computed, onBeforeUnmount, ref, watch } from 'vue'
import { useI18n } from 'vue-i18n'
import { keysApi } from '@/api'
import { errorMessage, type ApiError } from '@/api/client'
import { intlLocale } from '@/i18n'
import { useAppStore } from '@/stores/app'
import {
  CLIENT,
  connectSnippet,
  installCodeCommand,
  installCodeKind,
  isInstallCode,
  type InstallCodeKind,
  type Shell
} from '@/lib/connect'
import CopyButton from './CopyButton.vue'

const props = withDefaults(defineProps<{ apiKey: string; apiKeyId: number; supported?: boolean }>(), { supported: true })
const { t } = useI18n()
const app = useAppStore()

const defaultShell: Shell = /Win/i.test(navigator.platform || navigator.userAgent) ? 'powershell' : 'unix'
const shell = ref<Shell>(defaultShell)
const shells: Array<{ id: Shell; label: string }> = [
  { id: 'unix', label: 'macOS / Linux' },
  { id: 'powershell', label: 'PowerShell' },
  { id: 'cmd', label: 'CMD' }
]

const snippet = computed(() => connectSnippet(app.apiBaseUrl, props.apiKey, shell.value))
const envText = computed(() => snippet.value.envLines.join('\n'))

// ---- one-time install codes (admin switch; off = the panel exactly as before) ----

/**
 * Code lifetime (the server's AITPInstallCodeTTL), counted from the request start, which
 * precedes the server's SET. A code is hidden once EITHER the monotonic clock (immune to
 * wall-clock changes) OR the wall clock (keeps running while some browsers pause the
 * monotonic clock in system sleep) says 10 minutes have passed; a wrong but steady wall
 * clock changes neither.
 */
const CODE_TTL_MS = 10 * 60 * 1000
const monotonic = () => performance.now()

/** Short command instead of the full one, wherever the full one-line command would be offered. */
const codeMode = computed(() => app.aitpInstallCodeEnabled && !!snippet.value.command)

/** A generated code is bound to (key id, script kind); CMD and PowerShell share the PowerShell script. */
interface CodeSlot {
  seq: number
  loading: boolean
  code: string | null
  /** monotonic() deadline */
  deadline: number
  /** Wall-clock deadline, also shown as "valid until". */
  expiresAt: number
  error: string
}
const slots = ref<Record<string, CodeSlot>>({})
let requestSeq = 0
const slotKey = (keyId: number, kind: InstallCodeKind) => `${keyId}:${kind}`
const current = computed<CodeSlot | undefined>(() => slots.value[slotKey(props.apiKeyId, installCodeKind(shell.value))])

// Another key: drop every result (an in-flight response then finds no slot and is ignored).
watch(
  () => props.apiKeyId,
  () => {
    slots.value = {}
  }
)

const now = ref(monotonic())
const wallNow = ref(Date.now())
let ticker: number | undefined
let disposed = false
const tick = () => {
  now.value = monotonic()
  wallNow.value = Date.now()
}
/** Re-check expiry while a code is shown; started by the first code, so the off state runs no timer. */
function startTicker(): void {
  if (ticker !== undefined || disposed) return
  ticker = window.setInterval(tick, 5000)
  // Background tabs throttle timers: re-check as soon as the page is visible again.
  document.addEventListener('visibilitychange', tick)
}
onBeforeUnmount(() => {
  disposed = true
  window.clearInterval(ticker)
  document.removeEventListener('visibilitychange', tick)
})

const expired = computed(
  () => !!current.value?.code && (now.value >= current.value.deadline || wallNow.value >= current.value.expiresAt)
)
const shortCommand = computed(() =>
  current.value?.code && !expired.value ? installCodeCommand(shell.value, window.location.origin, current.value.code) : null
)
const expiresText = computed(() =>
  current.value?.code
    ? new Intl.DateTimeFormat(intlLocale(), { hour: '2-digit', minute: '2-digit', hour12: false }).format(new Date(current.value.expiresAt))
    : ''
)

function codeErrorText(e: unknown): string {
  switch ((e as Partial<ApiError> | undefined)?.reason) {
    case 'AITP_INSTALL_CODE_DISABLED':
      return t('connect.codeErrDisabled')
    case 'AITP_INSTALL_CODE_RATE_LIMITED':
      return t('connect.codeErrRateLimited')
    case 'AITP_INSTALL_CODE_BASE_URL_INVALID':
      return t('connect.codeErrBaseUrl')
    case 'AITP_INSTALL_CODE_KEY_UNUSABLE':
      return t('connect.codeErrKey')
    default:
      return errorMessage(e, t('connect.codeErrFailed'))
  }
}

async function generate(): Promise<void> {
  const keyId = props.apiKeyId
  const requestShell = shell.value
  const key = slotKey(keyId, installCodeKind(requestShell))
  const seq = ++requestSeq
  const startedMono = monotonic()
  const startedWall = Date.now()
  const previous = slots.value[key]
  slots.value = {
    ...slots.value,
    [key]: { seq, loading: true, code: previous?.code ?? null, deadline: previous?.deadline ?? 0, expiresAt: previous?.expiresAt ?? 0, error: '' }
  }
  // Only the latest request of the same (key, kind), on a still mounted panel, may write its
  // slot; other slots are never touched.
  const stillCurrent = () => !disposed && slots.value[key]?.seq === seq
  try {
    const res = await keysApi.installCode(keyId, requestShell)
    if (!stillCurrent()) return
    if (!isInstallCode(res?.code)) throw new Error('unexpected install code')
    tick()
    startTicker()
    slots.value = {
      ...slots.value,
      [key]: { seq, loading: false, code: res.code, deadline: startedMono + CODE_TTL_MS, expiresAt: startedWall + CODE_TTL_MS, error: '' }
    }
  } catch (e) {
    if (!stillCurrent()) return
    // A failed regenerate keeps the previous command: it stays valid until it expires.
    const prev = slots.value[key]
    slots.value = {
      ...slots.value,
      [key]: { seq, loading: false, code: prev?.code ?? null, deadline: prev?.deadline ?? 0, expiresAt: prev?.expiresAt ?? 0, error: codeErrorText(e) }
    }
  }
}
</script>

<template>
  <p v-if="!supported" class="text-sm text-ink-2" data-testid="connect-unsupported">{{ t('connect.unsupported') }}</p>
  <div v-else class="space-y-5">
    <div role="tablist" :aria-label="t('connect.shell')" class="flex gap-1 border-b border-rule">
      <button
        v-for="s in shells"
        :key="s.id"
        type="button"
        role="tab"
        :aria-selected="shell === s.id"
        class="-mb-px border-b-2 px-3 py-2 text-sm transition-colors duration-150"
        :class="shell === s.id ? 'border-blue text-blue' : 'border-transparent text-ink-2 hover:text-ink'"
        @click="shell = s.id"
      >
        {{ s.label }}
      </button>
    </div>

    <div v-if="codeMode" data-testid="connect-code">
      <h3 class="text-sm font-medium">{{ t('connect.oneLine', { client: CLIENT.displayName }) }}</h3>
      <p class="mt-1.5 text-xs text-ink-3">{{ t('connect.codeLead', { client: CLIENT.displayName }) }}</p>

      <div v-if="shortCommand" class="mt-3" data-testid="connect-code-command">
        <div class="flex items-baseline justify-between gap-3">
          <p class="text-xs text-ink-2" data-testid="connect-code-expiry">{{ t('connect.codeExpiresAt', { time: expiresText }) }}</p>
          <CopyButton :value="shortCommand" small />
        </div>
        <pre class="mt-2 overflow-x-auto whitespace-pre-wrap break-all rounded border border-rule bg-shell px-3 py-2.5 font-mono text-xs leading-5 text-ink">{{ shortCommand }}</pre>
        <p class="mt-2 text-xs text-ink-3">{{ t('connect.codeValidity') }} {{ t('connect.codeOlderStillValid') }}</p>
      </div>
      <p v-else-if="expired" class="mt-3 text-sm text-ink-2" data-testid="connect-code-expired">{{ t('connect.codeExpired') }}</p>

      <div class="mt-3">
        <button
          type="button"
          class="btn btn-sm"
          :class="shortCommand ? 'btn-line' : 'btn-primary'"
          :disabled="current?.loading"
          data-testid="connect-code-generate"
          @click="generate"
        >
          {{ current?.loading ? t('connect.codeGenerating') : current?.code ? t('connect.codeRegenerate') : t('connect.codeGenerate') }}
        </button>
      </div>
      <p v-if="current?.error" class="mt-2 text-sm text-seal" role="alert" data-testid="connect-code-error">{{ current.error }}</p>

      <p class="mt-3 text-sm text-ink">{{ t('connect.afterInstall') }}</p>
      <p class="mt-1.5 text-xs text-ink-3">
        {{ t('connect.oneLineHint', { client: CLIENT.displayName }) }}
        <a :href="CLIENT.repoUrl" target="_blank" rel="noopener noreferrer">{{ t('connect.about', { client: CLIENT.displayName }) }}</a>
        · {{ t('connect.uninstall') }} <code class="font-mono">{{ CLIENT.uninstallCommand }}</code>
      </p>

      <details class="group mt-3" data-testid="connect-full">
        <summary class="cursor-pointer select-none text-sm text-ink-2 hover:text-ink">{{ t('connect.codeFull') }}</summary>
        <div class="mt-3">
          <div class="flex justify-end">
            <CopyButton :value="snippet.command ?? ''" small />
          </div>
          <pre class="mt-2 overflow-x-auto whitespace-pre-wrap break-all rounded border border-rule bg-shell px-3 py-2.5 font-mono text-xs leading-5 text-ink">{{ snippet.command }}</pre>
        </div>
      </details>
    </div>

    <div v-else-if="snippet.command" data-testid="connect-install">
      <div class="flex items-baseline justify-between gap-3">
        <h3 class="text-sm font-medium">{{ t('connect.oneLine', { client: CLIENT.displayName }) }}</h3>
        <CopyButton :value="snippet.command" small />
      </div>
      <pre class="mt-2 overflow-x-auto whitespace-pre-wrap break-all rounded border border-rule bg-shell px-3 py-2.5 font-mono text-xs leading-5 text-ink">{{ snippet.command }}</pre>
      <p class="mt-2 text-sm text-ink">{{ t('connect.afterInstall') }}</p>
      <p class="mt-1.5 text-xs text-ink-3">
        {{ t('connect.oneLineHint', { client: CLIENT.displayName }) }}
        <a :href="CLIENT.repoUrl" target="_blank" rel="noopener noreferrer">{{ t('connect.about', { client: CLIENT.displayName }) }}</a>
        · {{ t('connect.uninstall') }} <code class="font-mono">{{ CLIENT.uninstallCommand }}</code>
      </p>
    </div>

    <p v-if="!snippet.command && CLIENT.oneLineInstall" class="text-sm text-ink-2" data-testid="connect-no-oneline">
      {{ snippet.badBaseUrl ? t('connect.badBaseUrl') : t('connect.noOneLine', { client: CLIENT.displayName }) }}
    </p>

    <details class="group" :open="!snippet.command" data-testid="connect-manual">
      <summary class="cursor-pointer select-none text-sm text-ink-2 hover:text-ink">
        {{ snippet.command ? t('connect.manual', { client: CLIENT.displayName }) : t('connect.manualOnly') }}
      </summary>
      <div class="mt-3">
        <div class="flex items-baseline justify-between gap-3">
          <p class="text-xs text-ink-3">{{ t('connect.manualHint') }}</p>
          <CopyButton :value="envText" small />
        </div>
        <pre class="mt-2 overflow-x-auto rounded border border-rule bg-shell px-3 py-2.5 font-mono text-xs leading-5 text-ink">{{ envText }}</pre>
        <dl class="mt-3 grid grid-cols-[auto_1fr] gap-x-6 gap-y-1 text-sm">
          <dt class="label self-center">Base URL</dt>
          <dd class="truncate font-mono text-xs">{{ snippet.baseUrl }}</dd>
        </dl>
      </div>
    </details>
  </div>
</template>
