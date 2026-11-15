<script setup lang="ts">
/** Cloudflare Turnstile, loaded only when the site enables it. */
import { onMounted, onUnmounted, ref } from 'vue'

interface TurnstileAPI {
  render: (el: HTMLElement, opts: Record<string, unknown>) => string
  reset: (id?: string) => void
  remove: (id?: string) => void
}

declare global {
  interface Window {
    turnstile?: TurnstileAPI
  }
}

const props = defineProps<{ siteKey: string }>()
const emit = defineEmits<{ verify: [token: string]; expire: [] }>()
const box = ref<HTMLElement | null>(null)
let widgetId: string | null = null

function load(): Promise<void> {
  if (window.turnstile) return Promise.resolve()
  return new Promise((resolve, reject) => {
    const existing = document.querySelector<HTMLScriptElement>('script[data-aog-turnstile]')
    const script = existing ?? document.createElement('script')
    script.addEventListener('load', () => resolve(), { once: true })
    script.addEventListener('error', () => reject(new Error('turnstile')), { once: true })
    if (!existing) {
      script.src = 'https://challenges.cloudflare.com/turnstile/v0/api.js?render=explicit'
      script.async = true
      script.dataset.aogTurnstile = '1'
      document.head.appendChild(script)
    }
  })
}

onMounted(async () => {
  try {
    await load()
  } catch {
    return
  }
  if (!box.value || !window.turnstile) return
  widgetId = window.turnstile.render(box.value, {
    sitekey: props.siteKey,
    theme: document.documentElement.classList.contains('dark') ? 'dark' : 'light',
    size: 'flexible',
    callback: (token: string) => emit('verify', token),
    'expired-callback': () => emit('expire'),
    'error-callback': () => emit('expire')
  })
})

onUnmounted(() => {
  if (widgetId && window.turnstile) window.turnstile.remove(widgetId)
})

defineExpose({
  reset: () => {
    if (widgetId && window.turnstile) window.turnstile.reset(widgetId)
  }
})
</script>

<template>
  <div ref="box" class="min-h-[65px]" />
</template>
