<script setup lang="ts">
import { ref } from 'vue'
import { useI18n } from 'vue-i18n'
import { getModerationEvidence, holdModerationEvidence, releaseModerationEvidence, type ModerationEvidence } from '@/api/admin/usage'
import { moderationCategoryLabel } from '@/utils/moderationLabels'
import { formatDateTime } from '@/utils/format'

// Original text is fetched only after an explicit confirmation, because every
// view is recorded server-side with the administrator's identity.
const props = defineProps<{ eventId: number }>()
const { t, locale } = useI18n()
const stage = ref<'idle' | 'confirm' | 'loading' | 'shown' | 'failed'>('idle')
const evidence = ref<ModerationEvidence | null>(null)
const error = ref('')
const holdDays = ref(90)
const holdReason = ref('')
const holdError = ref('')
const busy = ref(false)

function errorMessage(err: unknown, fallback: string) {
  const message = (err as { message?: string } | null)?.message
  return message ? `${fallback}: ${message}` : fallback
}

async function load() {
  stage.value = 'loading'
  error.value = ''
  try {
    evidence.value = await getModerationEvidence(props.eventId)
    stage.value = 'shown'
  } catch (err) {
    evidence.value = null
    error.value = errorMessage(err, t('admin.groups.inputModeration.evidence.loadFailed'))
    stage.value = 'failed'
  }
}

async function changeHold(hold: boolean) {
  if (!evidence.value) return
  busy.value = true
  holdError.value = ''
  try {
    if (hold) {
      const result = await holdModerationEvidence(props.eventId, holdDays.value, holdReason.value.trim())
      evidence.value = { ...evidence.value, hold_until: result.hold_until }
    } else {
      await releaseModerationEvidence(props.eventId, holdReason.value.trim())
      evidence.value = { ...evidence.value, hold_until: null }
    }
    holdReason.value = ''
  } catch (err) {
    holdError.value = errorMessage(err, t('admin.groups.inputModeration.evidence.holdFailed'))
  } finally {
    busy.value = false
  }
}
</script>

<template>
  <div class="space-y-2" data-testid="moderation-evidence">
    <button v-if="stage === 'idle'" type="button" class="btn btn-secondary btn-sm" @click="stage = 'confirm'">
      {{ t('admin.groups.inputModeration.evidence.view') }}
    </button>
    <div v-else-if="stage === 'confirm'" role="alertdialog" class="space-y-2 rounded border border-amber-300 p-2 dark:border-amber-700">
      <div class="font-medium">{{ t('admin.groups.inputModeration.evidence.confirmTitle') }}</div>
      <p class="text-xs">{{ t('admin.groups.inputModeration.evidence.confirmBody') }}</p>
      <div class="flex gap-2">
        <button type="button" class="btn btn-primary btn-sm" data-testid="evidence-confirm" @click="load">{{ t('admin.groups.inputModeration.evidence.confirm') }}</button>
        <button type="button" class="btn btn-secondary btn-sm" @click="stage = 'idle'">{{ t('admin.groups.inputModeration.evidence.cancel') }}</button>
      </div>
    </div>
    <p v-else-if="stage === 'loading'">{{ t('admin.groups.inputModeration.evidence.loading') }}</p>
    <p v-else-if="stage === 'failed'" role="alert" class="text-red-600">{{ error }}</p>
    <div v-else-if="evidence" class="space-y-3">
      <p v-if="evidence.truncated" class="text-xs text-amber-700 dark:text-amber-300" data-testid="evidence-truncated">
        {{ t('admin.groups.inputModeration.evidence.truncated') }}
        <template v-if="evidence.omitted_categories?.length">
          {{ t('admin.groups.inputModeration.evidence.omitted') }}: {{ evidence.omitted_categories.map(c => moderationCategoryLabel(c, locale)).join(', ') }}
        </template>
      </p>
      <article v-for="check in evidence.checks" :key="check.check_index" class="space-y-1 rounded border p-2 dark:border-dark-600">
        <div class="flex flex-wrap items-center gap-1 text-xs">
          <span v-for="kind in check.kinds" :key="kind" class="rounded bg-gray-100 px-1.5 py-0.5 dark:bg-dark-700">{{ t('admin.groups.inputModeration.evidence.kinds.' + kind) }}</span>
          <span v-for="(source, i) in check.sources" :key="i" class="text-gray-500">
            <template v-if="source.bundle">{{ t('admin.groups.inputModeration.evidence.bundle', { n: source.bundle }) }} · </template>{{ t('admin.groups.inputModeration.evidence.range', { start: source.start + 1, end: source.end }) }}
          </span>
          <span class="text-gray-500">{{ t('admin.groups.inputModeration.evidence.chars', { n: check.char_count }) }}</span>
        </div>
        <p v-if="check.kinds.includes('joint')" class="text-xs text-gray-500">{{ t('admin.groups.inputModeration.evidence.jointHint') }}</p>
        <div class="flex flex-wrap gap-1 text-xs">
          <span v-for="category in check.categories" :key="category" class="rounded bg-amber-100 px-1.5 py-0.5 text-amber-800 dark:bg-amber-900 dark:text-amber-200">
            {{ moderationCategoryLabel(category, locale) }} {{ ((check.scores[category] ?? 0) * 100).toFixed(1) }}%
          </span>
        </div>
        <pre class="max-h-64 overflow-auto whitespace-pre-wrap break-words rounded bg-gray-50 p-2 text-xs dark:bg-dark-800">{{ check.text }}</pre>
      </article>
      <div class="space-y-1 border-t pt-2 text-xs">
        <div>{{ t('admin.groups.inputModeration.evidence.expiresAt') }} {{ formatDateTime(evidence.expires_at) }}</div>
        <div v-if="evidence.hold_until" data-testid="evidence-hold">{{ t('admin.groups.inputModeration.evidence.holdUntil') }} {{ formatDateTime(evidence.hold_until) }}</div>
        <div class="flex flex-wrap items-end gap-2">
          <label class="flex flex-col">{{ t('admin.groups.inputModeration.evidence.holdDays') }}
            <input v-model.number="holdDays" type="number" min="1" max="365" class="input w-24" />
          </label>
          <label class="flex flex-1 flex-col">{{ t('admin.groups.inputModeration.evidence.holdReason') }}
            <input v-model="holdReason" type="text" maxlength="500" class="input" />
          </label>
          <button type="button" class="btn btn-secondary btn-sm" :disabled="busy || !holdReason.trim() || holdDays < 1 || holdDays > 365" @click="changeHold(true)">{{ t('admin.groups.inputModeration.evidence.hold') }}</button>
          <button v-if="evidence.hold_until" type="button" class="btn btn-secondary btn-sm" :disabled="busy" @click="changeHold(false)">{{ t('admin.groups.inputModeration.evidence.release') }}</button>
        </div>
        <p v-if="holdError" role="alert" class="text-red-600">{{ holdError }}</p>
      </div>
    </div>
  </div>
</template>
