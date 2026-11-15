<script setup lang="ts">
/** How to point Claude Code at this key: one AITP command, with the raw values tucked away. */
import { computed, ref } from 'vue'
import { useI18n } from 'vue-i18n'
import { useAppStore } from '@/stores/app'
import { CLIENT, connectSnippet, type Shell } from '@/lib/connect'
import CopyButton from './CopyButton.vue'

const props = withDefaults(defineProps<{ apiKey: string; supported?: boolean }>(), { supported: true })
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

    <div v-if="snippet.command" data-testid="connect-install">
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
