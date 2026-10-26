<template>
  <span v-if="total != null" class="inline-flex rounded bg-sky-50 px-1.5 py-0.5 text-[11px] font-medium text-sky-700 dark:bg-sky-500/10 dark:text-sky-300" data-testid="input-context-indicator" :title="t('usage.inputContextHint', { total: total.toLocaleString() })">
    {{ t('usage.inputContext') }} &gt;272K
  </span>
</template>

<script setup lang="ts">
import { computed } from 'vue'
import { useI18n } from 'vue-i18n'
import { usageInputContext } from '@/utils/usageLongContext'
const props = defineProps<{ row: Parameters<typeof usageInputContext>[0] }>()
const { t } = useI18n()
const total = computed(() => usageInputContext(props.row))
</script>
