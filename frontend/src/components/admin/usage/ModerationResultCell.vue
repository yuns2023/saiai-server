<script setup lang="ts">
import { computed, ref } from 'vue'
import { useI18n } from 'vue-i18n'
import { moderationCategoryLabel } from '@/utils/moderationLabels'
import BaseDialog from '@/components/common/BaseDialog.vue'
import type { AdminUsageLog } from '@/types'

const props = defineProps<{ row: Pick<AdminUsageLog, 'moderation_result' | 'moderation_events' | 'moderation_unavailable'> }>()
const { t, locale } = useI18n()
const categoryLabel = (category: string) => moderationCategoryLabel(category, locale.value)
const open = ref(false)
const status = computed(() => props.row.moderation_result?.status ?? 'flagged')
const highestRisk = computed(() => {
  if (status.value !== 'passed') return null
  const scores = Object.values(props.row.moderation_result?.scores ?? {})
    .filter(score => Number.isFinite(score) && score >= 0 && score <= 1)
  return scores.length ? Math.max(...scores) : null
})
const categories = computed(() => {
  if (props.row.moderation_result) {
    if (status.value !== 'flagged') return []
    const result = props.row.moderation_result
    if (result.scores) return Object.entries(result.scores)
      .filter(([category, score]) => Number.isFinite(score) && score * 100 >=
        (result.decision_snapshot?.policy.thresholds[category] ?? result.min_confidence))
      .map(([category]) => category)
  }
  return [...new Set(props.row.moderation_events?.flatMap(event => event.categories) ?? [])]
})
</script>

<template>
  <span v-if="row.moderation_unavailable" class="text-xs text-gray-400">{{ t('admin.groups.inputModeration.unavailable') }}</span>
  <button v-else-if="row.moderation_result || row.moderation_events?.length" type="button"
    class="max-w-[280px] space-y-1 rounded text-left text-xs focus-visible:outline focus-visible:outline-2 focus-visible:outline-primary-500"
    :aria-label="t('admin.groups.inputModeration.viewDetails')" aria-haspopup="dialog" :aria-expanded="open" @click.stop="open = true">
    <span class="block underline decoration-dotted underline-offset-4"
      :class="status === 'passed' ? 'text-emerald-600 dark:text-emerald-400' : status === 'flagged' ? 'text-amber-700 dark:text-amber-300' : 'text-gray-500'">
      {{ status === 'passed' && row.moderation_result?.reason === 'chunked_input' ? t('admin.groups.inputModeration.chunkedPassed') : t('admin.groups.inputModeration.statuses.' + status) }}
      <span v-if="highestRisk !== null" data-testid="highest-risk"> · {{ t('admin.groups.inputModeration.highestRisk') }} {{ (highestRisk * 100).toFixed(1) }}%</span>
    </span>
    <span v-if="categories.length" class="flex flex-wrap gap-1">
      <span v-for="category in categories" :key="category" class="rounded bg-amber-100 px-1.5 py-0.5 text-amber-800 dark:bg-amber-900 dark:text-amber-200">{{ categoryLabel(category) }}</span>
    </span>
  </button>
  <span v-else class="text-xs text-gray-400" :title="t('admin.groups.inputModeration.asyncHint')">{{ t('admin.groups.inputModeration.noHit') }}</span>
  <BaseDialog v-if="open" :show="open" :title="t('admin.groups.inputModeration.viewDetails')" @close="open = false">
    <span v-if="row.moderation_unavailable" class="text-xs text-gray-400">{{ t('admin.groups.inputModeration.unavailable') }}</span>
    <div v-else-if="row.moderation_result" class="space-y-2 text-sm">
      <span>{{ t('admin.groups.inputModeration.statuses.' + row.moderation_result.status) }}</span>
      <div v-if="row.moderation_result.reason" class="text-gray-500">{{ t('admin.groups.inputModeration.reasons.' + row.moderation_result.reason) }}</div>
      <div v-for="(score, category) in row.moderation_result.scores" :key="category">
        {{ categoryLabel(category) }} {{ (score * 100).toFixed(1) }}%<span v-if="row.moderation_result.decision_snapshot" class="text-gray-500"> / {{ row.moderation_result.decision_snapshot.policy.thresholds[category] }}%</span>
      </div>
      <div v-if="row.moderation_result.decision_snapshot" class="text-gray-500">{{ row.moderation_result.decision_snapshot.policy.rules_version }}</div>
      <div v-if="row.moderation_result.scores && !row.moderation_result.decision_snapshot" class="text-gray-500">{{ t('admin.groups.inputModeration.resultThreshold') }} {{ row.moderation_result.min_confidence }}%</div>
      <div v-for="(event, index) in row.moderation_events" :key="index" class="text-gray-500">
        {{ t('admin.groups.inputModeration.actions.' + event.action) }}<template v-if="event.counted_as_strike"> · {{ t('admin.groups.inputModeration.counted') }}</template>
      </div>
    </div>
    <div v-else-if="row.moderation_events?.length" class="space-y-2 text-sm">
      <div v-for="(event, index) in row.moderation_events" :key="index">
        <span v-for="category in event.categories" :key="category" class="mr-1 inline-block rounded bg-amber-100 px-1.5 py-0.5 text-amber-800 dark:bg-amber-900 dark:text-amber-200">
          {{ categoryLabel(category) }}<span v-if="event.scores?.[category] != null" class="ml-1">{{ (event.scores[category] * 100).toFixed(1) }}%</span>
        </span>
        <div class="text-gray-500">{{ t('admin.groups.inputModeration.actions.' + event.action) }}<template v-if="event.counted_as_strike"> · {{ t('admin.groups.inputModeration.counted') }}</template></div>
      </div>
    </div>
    <span v-else class="text-xs text-gray-400" :title="t('admin.groups.inputModeration.asyncHint')">{{ t('admin.groups.inputModeration.noHit') }}</span>
  </BaseDialog>
</template>
