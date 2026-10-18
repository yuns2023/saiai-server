<template>
  <div ref="container" class="relative" @keydown.esc="onEscape">
    <button type="button" class="btn btn-secondary flex items-center gap-2" :aria-expanded="open" @click="toggle">
      <Icon name="calendar" size="sm" />
      {{ t(presetLabels[preset || ''] || 'dates.custom') }}
      <Icon name="chevronDown" size="sm" />
    </button>
    <div v-if="open" class="absolute left-0 z-100 mt-2 w-[min(380px,calc(100vw-3rem))] rounded-xl border border-gray-200 bg-white p-3 shadow-lg dark:border-dark-600 dark:bg-dark-800">
      <div class="grid grid-cols-2 gap-1">
        <button v-for="(label, key) in presetLabels" :key="key" type="button" class="rounded-md px-3 py-2 text-sm hover:bg-gray-100 dark:hover:bg-dark-700" :class="draftPreset === key && 'bg-primary-100 text-primary-700'" @click="choose(key)">{{ t(label) }}</button>
      </div>
      <div class="mt-3 space-y-3 border-t border-gray-100 pt-3 dark:border-dark-600">
        <label class="block text-xs text-gray-500">{{ t('dates.startTime') }}
          <input v-model="start" type="datetime-local" step="60" class="input mt-1 w-full" @input="draftPreset = null" />
        </label>
        <label class="block text-xs text-gray-500">{{ t('dates.endTime') }}
          <input v-model="end" type="datetime-local" step="60" class="input mt-1 w-full" @input="draftPreset = null" />
        </label>
        <p class="text-xs text-gray-500">{{ timezone }} · {{ t('dates.endExclusive') }}</p>
        <p v-if="error" role="alert" class="text-xs text-red-600">{{ error }}</p>
        <div class="flex justify-end"><button type="button" class="btn btn-primary" :disabled="!!error" @click="apply">{{ t('dates.apply') }}</button></div>
      </div>
    </div>
  </div>
</template>

<script setup lang="ts">
import { computed, ref } from 'vue'
import { onClickOutside } from '@vueuse/core'
import { useI18n } from 'vue-i18n'
import Icon from '@/components/icons/Icon.vue'
import { localDateTime, presetTimeRange, type DateTimeRange } from '@/utils/dateTimeRange'

const props = defineProps<{ startTime: string; endTime: string; preset: string | null; maxDays?: number }>()
const emit = defineEmits<{ change: [range: DateTimeRange] }>()
const { t } = useI18n()
const container = ref<HTMLElement | null>(null)
const open = ref(false)
const start = ref('')
const end = ref('')
const draftPreset = ref<string | null>(null)
const timezone = Intl.DateTimeFormat().resolvedOptions().timeZone || 'UTC'
const presetLabels: Record<string, string> = {
  today: 'dates.today', yesterday: 'dates.yesterday', last24Hours: 'dates.last24Hours',
  '7days': 'dates.last7Days', '14days': 'dates.last14Days', '30days': 'dates.last30Days',
  thisMonth: 'dates.thisMonth', lastMonth: 'dates.lastMonth'
}
const error = computed(() => {
  const duration = new Date(end.value).getTime() - new Date(start.value).getTime()
  if (!Number.isFinite(duration) || duration <= 0) return t('dates.invalidTimeRange')
  if (props.maxDays && duration > props.maxDays * 86400000) return t('dates.maxRangeDays', { days: props.maxDays })
  return ''
})
function setDraft(range: DateTimeRange) {
  start.value = localDateTime(new Date(range.startTime))
  end.value = localDateTime(new Date(range.endTime))
  draftPreset.value = range.preset
}
function toggle() {
  if (!open.value) setDraft(props)
  open.value = !open.value
}
function onEscape(event: KeyboardEvent) {
  if (!open.value) return
  event.stopPropagation()
  open.value = false
}
function choose(preset: string) { setDraft(presetTimeRange(preset)) }
function apply() {
  if (error.value) return
  emit('change', { startTime: new Date(start.value).toISOString(), endTime: new Date(end.value).toISOString(), preset: draftPreset.value })
  open.value = false
}
onClickOutside(container, () => { open.value = false })
</script>
