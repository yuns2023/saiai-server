<script setup lang="ts">
/** How to point a coding tool at this key: one aog command, or the raw values. */
import { computed, ref } from 'vue'
import { useI18n } from 'vue-i18n'
import { useAppStore } from '@/stores/app'
import { CLIENT, connectSnippet, type Shell } from '@/lib/connect'
import CopyButton from './CopyButton.vue'

const props = defineProps<{ apiKey: string; platform: string }>()
const { t } = useI18n()
const app = useAppStore()

const defaultShell: Shell = /Win/i.test(navigator.platform || navigator.userAgent) ? 'powershell' : 'unix'
const shell = ref<Shell>(defaultShell)
const shells: Array<{ id: Shell; label: string }> = [
  { id: 'unix', label: 'macOS / Linux' },
  { id: 'powershell', label: 'PowerShell' },
  { id: 'cmd', label: 'CMD' }
]

const snippet = computed(() => connectSnippet(props.platform, app.apiBaseUrl, props.apiKey, shell.value))
const toolName = computed(() =>
  props.platform === 'openai' ? 'Codex' : props.platform === 'gemini' ? 'Gemini CLI' : 'Claude Code'
)
const envText = computed(() => snippet.value.envLines.join('\n'))
</script>

<template>
  <div class="space-y-5">
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

    <div v-if="snippet.command">
      <div class="flex items-baseline justify-between gap-3">
        <h3 class="text-sm font-medium">{{ t('connect.oneLine', { client: CLIENT.displayName, tool: toolName }) }}</h3>
        <CopyButton :value="snippet.command" small />
      </div>
      <pre class="mt-2 overflow-x-auto whitespace-pre-wrap break-all rounded border border-rule bg-shell px-3 py-2.5 font-mono text-xs leading-5 text-ink">{{ snippet.command }}</pre>
      <p class="mt-1.5 text-xs text-ink-3">{{ t('connect.oneLineHint', { client: CLIENT.displayName, tool: toolName }) }}</p>
    </div>

    <div>
      <div class="flex items-baseline justify-between gap-3">
        <h3 class="text-sm font-medium">{{ snippet.command ? t('connect.manual') : t('connect.manualOnly', { tool: toolName }) }}</h3>
        <CopyButton :value="envText" small />
      </div>
      <pre class="mt-2 overflow-x-auto rounded border border-rule bg-shell px-3 py-2.5 font-mono text-xs leading-5 text-ink">{{ envText }}</pre>
      <dl class="mt-3 grid grid-cols-[auto_1fr] gap-x-6 gap-y-1 text-sm">
        <dt class="label self-center">Base URL</dt>
        <dd class="truncate font-mono text-xs">{{ snippet.baseUrl }}</dd>
      </dl>
    </div>
  </div>
</template>
