<script setup lang="ts">
import { ref } from 'vue'
import { useI18n } from 'vue-i18n'
import { useAppStore } from '@/stores/app'
import Icon from './Icon.vue'

const props = defineProps<{ value: string; label?: string; small?: boolean }>()
const { t } = useI18n()
const app = useAppStore()
const copied = ref(false)
let timer: number | undefined

async function copy(): Promise<void> {
  try {
    await navigator.clipboard.writeText(props.value)
  } catch {
    // Insecure context fallback.
    const area = document.createElement('textarea')
    area.value = props.value
    area.setAttribute('readonly', '')
    area.style.position = 'fixed'
    area.style.opacity = '0'
    document.body.appendChild(area)
    area.select()
    const ok = document.execCommand('copy')
    area.remove()
    if (!ok) {
      app.notify('error', t('common.copyFailed'))
      return
    }
  }
  copied.value = true
  window.clearTimeout(timer)
  timer = window.setTimeout(() => (copied.value = false), 1800)
}
</script>

<template>
  <button type="button" class="btn btn-line" :class="small && 'btn-sm'" @click="copy">
    <Icon :name="copied ? 'check' : 'copy'" :size="small ? 14 : 16" :class="copied && 'text-ok'" />
    <span>{{ copied ? t('common.copied') : (label ?? t('common.copy')) }}</span>
  </button>
</template>
