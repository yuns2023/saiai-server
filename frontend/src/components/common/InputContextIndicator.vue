<template>
  <span v-if="info" class="inline-flex rounded bg-sky-50 px-1.5 py-0.5 text-[11px] font-medium text-sky-700 dark:bg-sky-500/10 dark:text-sky-300" data-testid="input-context-indicator" :title="t('usage.inputContextHint', { total: info.total.toLocaleString(), threshold: info.threshold.toLocaleString() })">
    {{ t('usage.inputContext') }} &gt;{{ thresholdLabel }}
  </span>
</template>

<script setup lang="ts">
import { computed } from 'vue'
import { useI18n } from 'vue-i18n'
import { usageInputContext } from '@/utils/usageLongContext'
const props = defineProps<{ row: Parameters<typeof usageInputContext>[0] }>()
const { t } = useI18n()
const info = computed(() => usageInputContext(props.row))
const thresholdLabel = computed(() => info.value && info.value.threshold % 1000 === 0 ? `${info.value.threshold / 1000}K` : info.value?.threshold.toLocaleString())
</script>
