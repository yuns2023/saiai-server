<script setup lang="ts">
import { computed, onUnmounted, ref, watch } from 'vue'
import { useI18n } from 'vue-i18n'
import BaseDialog from '@/components/common/BaseDialog.vue'
import Toggle from '@/components/common/Toggle.vue'
import { groupsAPI } from '@/api/admin/groups'
import { opsAPI, type OpsLogFilterConfig, type OpsLogFilterReason, type OpsLogFilterRule } from '@/api/admin/ops'
import {
  copyLogFilterRule,
  createLogFilterPreset,
  isGlobalLogFilter,
  isLogFilterConflict,
  isPreIdentityKeyFilter,
  legacyLogFilters,
  localAuthReasons,
  logFilterPlatforms,
  mergeLegacyLogFilters,
  prefillLogFilterProposal,
  validateLogFilterRule,
  type LegacyLogFilters
} from '../utils/opsLogFilters'

const props = defineProps<{
  show: boolean
  platform?: string
  groupId?: number | null
  errorId?: number | null
}>()
const emit = defineEmits<{ close: []; saved: [] }>()
const { t: translate } = useI18n()
const config = ref<OpsLogFilterConfig | null>(null)
const rules = ref<OpsLogFilterRule[]>([])
const legacy = ref<LegacyLogFilters | null>(null)
const groups = ref<Array<{ id: number; name: string; platform: string }>>([])
const groupsError = ref('')
const loading = ref(false)
const saving = ref(false)
const legacySaving = ref(false)
const loadError = ref('')
const saveError = ref('')
const legacyError = ref('')
const notice = ref('')
const conflict = ref(false)
const editor = ref<OpsLogFilterRule | null>(null)
const editingId = ref<string | null>(null)
const editorError = ref('')
const scopeMode = ref<'group' | 'platform' | 'global'>('global')
const scopeGroup = ref<number | string>('')
const scopePlatform = ref('')
const statusText = ref('')
const keywordText = ref('')
const globalConfirmed = ref(false)
const proposalVerified = ref<boolean | null>(null)
let loadSequence = 0
let controller: AbortController | null = null

const busy = computed(() => loading.value || saving.value || legacySaving.value)
const candidateScope = computed(() => ({
  group_id: scopeMode.value === 'group' ? Number(scopeGroup.value) : undefined,
  platform: scopeMode.value === 'global' ? undefined : scopePlatform.value.trim() || undefined
}))
const requiresConfirmation = computed(() => isGlobalLogFilter(candidateScope.value))
const keyReasonNeedsPlatform = computed(() => !!editor.value && isPreIdentityKeyFilter(editor.value))
const hasPendingRules = computed(() => !!config.value && JSON.stringify(rules.value) !== JSON.stringify(config.value.rules.map(copyLogFilterRule)))
const groupOptions = computed(() => {
  const options = groups.value.map(group => ({ id: group.id, label: `${group.name} (#${group.id}) · ${platformLabel(group.platform)}` }))
  const selectedId = Number(scopeGroup.value)
  if (selectedId > 0 && !options.some(group => group.id === selectedId)) {
    options.push({ id: selectedId, label: translate('admin.ops.logFilters.groupScope', { id: selectedId }) })
  }
  return options
})

function platformLabel(platform: string): string {
  return logFilterPlatforms.find(option => option.value === platform)?.label ?? platform
}

function close() {
  if (!saving.value && !legacySaving.value) emit('close')
}

function openEditor(rule: OpsLogFilterRule, existing = false) {
  editor.value = copyLogFilterRule(rule)
  editingId.value = existing ? rule.id : null
  scopeMode.value = rule.group_id != null ? 'group' : rule.platform ? 'platform' : 'global'
  scopeGroup.value = rule.group_id ?? props.groupId ?? ''
  scopePlatform.value = rule.platform ?? ''
  statusText.value = (rule.status_codes ?? []).join(', ')
  keywordText.value = (rule.keywords ?? []).join('\n')
  globalConfirmed.value = false
  proposalVerified.value = null
  editorError.value = ''
  notice.value = ''
}

function createPreset(reason: OpsLogFilterReason) {
  const selectedGroup = groups.value.find(group => group.id === props.groupId)
  openEditor(createLogFilterPreset(reason, translate(`admin.ops.logFilters.reasons.${reason}`), {
    group_id: props.groupId,
    platform: props.platform?.trim() || selectedGroup?.platform
  }))
}

function scopeLabel(rule: OpsLogFilterRule): string {
  const labels: string[] = []
  if (rule.group_id != null) {
    const group = groups.value.find(group => group.id === rule.group_id)
    labels.push(group ? `${group.name} (#${group.id})` : translate('admin.ops.logFilters.groupScope', { id: rule.group_id }))
  }
  if (rule.platform) labels.push(platformLabel(rule.platform))
  return labels.join(' · ') || translate('admin.ops.logFilters.globalScope')
}

function sourceLabel(rule: OpsLogFilterRule): string {
  return rule.source === 'local_auth'
    ? `${translate('admin.ops.logFilters.localAuth')} · ${translate(`admin.ops.logFilters.reasons.${rule.reason}`)}`
    : `${translate('admin.ops.logFilters.upstream')} · ${(rule.status_codes ?? []).join(', ')} · ${(rule.keywords ?? []).join(' / ')}`
}

function toggleRule(rule: OpsLogFilterRule, enabled: boolean) {
  if (enabled && isGlobalLogFilter(rule)) {
    openEditor({ ...rule, enabled }, true)
    return
  }
  rule.enabled = enabled
  notice.value = ''
}

function removeRule(ruleId: string) {
  rules.value = rules.value.filter(rule => rule.id !== ruleId)
  if (editingId.value === ruleId) editor.value = null
  notice.value = ''
}

async function applyEditor() {
  if (!editor.value) return
  const candidate = copyLogFilterRule(editor.value)
  if (!editingId.value) candidate.enabled = true
  candidate.name = candidate.name.trim()
  candidate.group_id = candidateScope.value.group_id
  candidate.platform = candidateScope.value.platform
  if (scopeMode.value === 'platform' && !candidate.platform) {
    editorError.value = translate('admin.ops.logFilters.validation.invalidPlatform')
    return
  }
  candidate.status_codes = statusText.value.trim() ? statusText.value.split(/[\s,]+/).map(Number) : []
  candidate.keywords = keywordText.value.trim() ? keywordText.value.split('\n').map(keyword => keyword.trim()).filter(Boolean) : []
  if (candidate.source === 'upstream') candidate.reason = undefined
  const validation = validateLogFilterRule(candidate, globalConfirmed.value)
  if (validation) {
    editorError.value = translate(`admin.ops.logFilters.validation.${validation}`)
    return
  }
  if (!editingId.value && rules.value.length >= 50) {
    editorError.value = translate('admin.ops.logFilters.validation.ruleLimit')
    return
  }
  const nextRules = editingId.value
    ? rules.value.map(rule => rule.id === editingId.value ? candidate : rule)
    : [...rules.value, candidate]
  if (await persistRules(nextRules)) editor.value = null
}

function acceptConfig(next: OpsLogFilterConfig) {
  config.value = next
  rules.value = next.rules.map(copyLogFilterRule)
}

async function load(includeProposal = true) {
  controller?.abort()
  controller = new AbortController()
  const sequence = ++loadSequence
  loading.value = true
  config.value = null
  legacy.value = null
  editor.value = null
  loadError.value = ''
  saveError.value = ''
  legacyError.value = ''
  groupsError.value = ''
  notice.value = ''
  conflict.value = false
  const results = await Promise.allSettled([
    opsAPI.getLogFilters({ signal: controller.signal }),
    opsAPI.getAdvancedSettings(),
    includeProposal && props.errorId
      ? opsAPI.getLogFilterProposal(props.errorId, { signal: controller.signal })
      : Promise.resolve(null),
    groupsAPI.getAll()
  ])
  if (sequence !== loadSequence || !props.show) return
  const [filtersResult, legacyResult, proposalResult, groupsResult] = results
  if (filtersResult.status === 'fulfilled') acceptConfig(filtersResult.value)
  else loadError.value = translate('admin.ops.logFilters.loadFailed')
  if (legacyResult.status === 'fulfilled') legacy.value = { ...legacyResult.value }
  else legacyError.value = translate('admin.ops.logFilters.legacyLoadFailed')
  if (groupsResult.status === 'fulfilled') groups.value = groupsResult.value.map(group => ({ id: group.id, name: group.name, platform: group.platform }))
  else {
    groups.value = []
    groupsError.value = translate('admin.ops.logFilters.groupsLoadFailed')
  }
  if (proposalResult.status === 'rejected') {
    notice.value = translate('admin.ops.logFilters.proposalFailed')
  } else if (proposalResult.value && config.value) {
    const candidate = prefillLogFilterProposal(proposalResult.value)
    if (candidate) {
      if (candidate.source === 'local_auth') candidate.name = translate(`admin.ops.logFilters.reasons.${candidate.reason}`)
      openEditor(candidate)
      proposalVerified.value = proposalResult.value.verified
    } else {
      notice.value = translate('admin.ops.logFilters.noProposal')
    }
  }
  loading.value = false
}

async function persistRules(nextRules: OpsLogFilterRule[]): Promise<boolean> {
  if (!config.value || busy.value || conflict.value) return false
  const invalid = nextRules.map(rule => validateLogFilterRule(rule, true)).find(Boolean)
  if (invalid || nextRules.length > 50 || new Set(nextRules.map(rule => rule.id)).size !== nextRules.length) {
    saveError.value = translate(`admin.ops.logFilters.validation.${invalid ?? 'ruleLimit'}`)
    return false
  }
  saving.value = true
  saveError.value = ''
  notice.value = ''
  try {
    acceptConfig(await opsAPI.updateLogFilters({ rules: nextRules.map(copyLogFilterRule), revision: config.value.revision }))
    notice.value = translate('admin.ops.logFilters.saved')
    emit('saved')
    return true
  } catch (error: unknown) {
    conflict.value = isLogFilterConflict(error)
    saveError.value = translate(conflict.value ? 'admin.ops.logFilters.conflict' : 'admin.ops.logFilters.saveFailed')
    return false
  } finally {
    saving.value = false
  }
}

async function saveRules() {
  if (!editor.value) await persistRules(rules.value)
}

async function saveLegacy() {
  if (!legacy.value || busy.value) return
  legacySaving.value = true
  legacyError.value = ''
  try {
    const current = await opsAPI.getAdvancedSettings()
    legacy.value = await opsAPI.updateAdvancedSettings(mergeLegacyLogFilters(current, legacy.value))
    notice.value = translate('admin.ops.logFilters.legacySaved')
    emit('saved')
  } catch {
    legacyError.value = translate('admin.ops.logFilters.legacySaveFailed')
  } finally {
    legacySaving.value = false
  }
}

watch(() => [props.show, props.errorId] as const, ([show]) => {
  if (show) void load()
  else {
    ++loadSequence
    controller?.abort()
  }
}, { immediate: true })

watch([scopeMode, scopeGroup, scopePlatform], () => { globalConfirmed.value = false })
watch(() => editor.value?.source, (source, previousSource) => {
  globalConfirmed.value = false
  if (previousSource && source !== previousSource) proposalVerified.value = null
  if (source === 'upstream' && editor.value) editor.value.match_mode = 'all'
})
watch([() => editor.value?.reason, statusText, keywordText], () => { globalConfirmed.value = false })
onUnmounted(() => {
  ++loadSequence
  controller?.abort()
})
</script>

<template>
  <BaseDialog :show="show" :title="translate('admin.ops.logFilters.title')" width="wide" @close="close">
    <div class="space-y-5 text-sm text-gray-700 dark:text-gray-300">
      <p class="rounded-xl bg-blue-50 p-3 text-blue-800 dark:bg-blue-900/20 dark:text-blue-300">{{ translate('admin.ops.logFilters.futureOnly') }}</p>
      <p class="text-xs text-gray-500">{{ translate('admin.ops.logFilters.countScope') }}</p>
      <p v-if="loading" role="status">{{ translate('common.loading') }}</p>
      <div v-if="loadError || saveError" role="alert" class="rounded-xl bg-red-50 p-3 text-red-700 dark:bg-red-900/20 dark:text-red-300">
        <p>{{ loadError || saveError }}</p>
        <button v-if="loadError || conflict" type="button" class="btn btn-secondary mt-2" data-testid="reload" :disabled="busy" @click="load(false)">{{ translate('admin.ops.logFilters.reload') }}</button>
      </div>
      <p v-if="notice" role="status">{{ notice }}</p>
      <p v-if="groupsError" role="status" class="text-amber-700 dark:text-amber-300">{{ groupsError }}</p>
      <p v-if="hasPendingRules" role="status" class="rounded-xl bg-amber-50 p-3 text-amber-800 dark:bg-amber-900/20 dark:text-amber-300">{{ translate('admin.ops.logFilters.pendingChanges') }}</p>
      <fieldset v-if="config && !loading" :disabled="busy || conflict" class="space-y-5">
        <div class="flex flex-wrap gap-2">
          <button v-for="reason in localAuthReasons.slice(0, 2)" :key="reason" type="button" class="btn btn-secondary" :data-testid="`preset-${reason}`" :disabled="rules.length >= 50" @click="createPreset(reason)">
            {{ translate('admin.ops.logFilters.addPreset', { name: translate(`admin.ops.logFilters.reasons.${reason}`) }) }}
          </button>
        </div>
        <p class="text-xs text-gray-500">{{ translate('admin.ops.logFilters.presetsDisabled') }}</p>
        <p v-if="!rules.length">{{ translate('admin.ops.logFilters.empty') }}</p>
        <div v-for="rule in rules" :key="rule.id" class="flex flex-wrap items-center justify-between gap-3 rounded-xl border border-gray-200 p-3 dark:border-dark-700" :data-testid="`rule-${rule.id}`">
          <div class="min-w-0 flex-1">
            <p class="font-semibold break-words">{{ rule.name }}</p>
            <p class="mt-1 break-words text-xs">{{ sourceLabel(rule) }} · {{ scopeLabel(rule) }}</p>
            <p class="mt-1 text-xs text-gray-500">{{ translate('admin.ops.logFilters.skippedCount', { count: config.counts[rule.id] ?? 0 }) }}</p>
          </div>
          <Toggle :model-value="rule.enabled" :aria-label="translate('admin.ops.logFilters.enableRule', { name: rule.name })" @update:model-value="toggleRule(rule, $event)" />
          <button type="button" class="btn btn-secondary" @click="openEditor(rule, true)">{{ translate('common.edit') }}</button>
          <button type="button" class="btn btn-secondary" @click="removeRule(rule.id)">{{ translate('admin.ops.logFilters.removeRule') }}</button>
        </div>
        <p class="text-xs text-gray-500">{{ translate('admin.ops.logFilters.disableHint') }}</p>
        <section v-if="editor" class="space-y-4 rounded-xl border border-primary-300 p-4 dark:border-primary-700" data-testid="editor">
          <h4 class="font-semibold">{{ translate('admin.ops.logFilters.confirmRule') }}</h4>
          <p v-if="proposalVerified === false" class="text-amber-700 dark:text-amber-300">{{ translate(editor.source === 'local_auth' ? 'admin.ops.logFilters.unverified' : 'admin.ops.logFilters.unverifiedUpstream') }}</p>
          <p v-else-if="proposalVerified === true">{{ translate(editor.source === 'local_auth' ? 'admin.ops.logFilters.verified' : 'admin.ops.logFilters.verifiedUpstream') }}</p>
          <label class="block">
            <span class="input-label">{{ translate('admin.ops.logFilters.name') }}</span>
            <input v-model="editor.name" class="input" maxlength="120" data-testid="rule-name" />
          </label>
          <p>{{ sourceLabel(editor) }}</p>
          <label v-if="editor.source === 'local_auth'" class="block">
            <span class="input-label">{{ translate('admin.ops.logFilters.reason') }}</span>
            <select v-model="editor.reason" class="input" data-testid="reason">
              <option v-for="reason in localAuthReasons" :key="reason" :value="reason">{{ translate(`admin.ops.logFilters.reasons.${reason}`) }}</option>
            </select>
          </label>
          <label class="block">
            <span class="input-label">{{ translate('admin.ops.logFilters.scope') }}</span>
            <select v-model="scopeMode" class="input" data-testid="scope">
              <option value="group" :disabled="keyReasonNeedsPlatform">{{ translate('admin.ops.logFilters.group') }}</option>
              <option value="platform">{{ translate('admin.ops.logFilters.platform') }}</option>
              <option value="global">{{ translate('admin.ops.logFilters.globalScope') }}</option>
            </select>
          </label>
          <p v-if="keyReasonNeedsPlatform" class="text-amber-700 dark:text-amber-300">{{ translate('admin.ops.logFilters.keyReasonScopeHint') }}</p>
          <label v-if="scopeMode === 'group'" class="block">
            <span class="input-label">{{ translate('admin.ops.logFilters.group') }}</span>
            <select v-model="scopeGroup" class="input" :disabled="keyReasonNeedsPlatform" data-testid="group">
              <option value="">{{ translate('admin.ops.logFilters.selectGroup') }}</option>
              <option v-for="group in groupOptions" :key="group.id" :value="group.id">{{ group.label }}</option>
            </select>
          </label>
          <label v-if="scopeMode !== 'global'" class="block">
            <span class="input-label">{{ translate(scopeMode === 'group' ? 'admin.ops.logFilters.optionalPlatform' : 'admin.ops.logFilters.platform') }}</span>
            <select v-model="scopePlatform" class="input" data-testid="platform">
              <option value="">{{ scopeMode === 'group' ? translate('common.all') : translate('admin.ops.logFilters.selectPlatform') }}</option>
              <option v-for="option in logFilterPlatforms" :key="option.value" :value="option.value">{{ option.label }}</option>
            </select>
          </label>
          <label v-if="requiresConfirmation" class="flex items-start gap-2 text-amber-700 dark:text-amber-300">
            <input v-model="globalConfirmed" type="checkbox" class="mt-1" data-testid="confirm-global" />
            {{ translate('admin.ops.logFilters.confirmGlobal') }}
          </label>
          <details :open="editor.source === 'upstream'" class="space-y-3">
            <summary class="cursor-pointer font-medium">{{ translate('admin.ops.logFilters.advanced') }}</summary>
            <p class="mt-3 text-amber-700 dark:text-amber-300">{{ translate('admin.ops.logFilters.upstreamWarning') }}</p>
            <label class="mt-3 block">
              <span class="input-label">{{ translate('admin.ops.logFilters.source') }}</span>
              <select v-model="editor.source" class="input" data-testid="source">
                <option value="local_auth">{{ translate('admin.ops.logFilters.localAuth') }}</option>
                <option value="upstream">{{ translate('admin.ops.logFilters.upstream') }}</option>
              </select>
            </label>
            <label class="mt-3 block">
              <span class="input-label">{{ translate('admin.ops.logFilters.statuses') }}</span>
              <input v-model="statusText" class="input" placeholder="400, 403" data-testid="statuses" />
            </label>
            <label class="mt-3 block">
              <span class="input-label">{{ translate('admin.ops.logFilters.keywords') }}</span>
              <textarea v-model="keywordText" class="input" rows="3" data-testid="keywords" />
            </label>
            <label class="mt-3 block">
              <span class="input-label">{{ translate('admin.ops.logFilters.matchMode') }}</span>
              <select v-model="editor.match_mode" class="input" :disabled="editor.source === 'upstream'" data-testid="match-mode">
                <option value="all">{{ translate('admin.ops.logFilters.matchAll') }}</option>
                <option v-if="editor.source === 'local_auth'" value="any">{{ translate('admin.ops.logFilters.matchAny') }}</option>
              </select>
            </label>
          </details>
          <label v-if="editingId" class="flex items-center justify-between gap-3">
            {{ translate('admin.ops.logFilters.enabled') }}
            <Toggle v-model="editor.enabled" :aria-label="translate('admin.ops.logFilters.enabled')" data-testid="editor-enabled" />
          </label>
          <p v-if="editorError" role="alert" class="text-red-600 dark:text-red-400">{{ editorError }}</p>
          <div class="flex gap-2">
            <button type="button" class="btn btn-primary" data-testid="apply-rule" @click="applyEditor">{{ saving ? translate('common.saving') : translate(editingId ? 'admin.ops.logFilters.saveRule' : 'admin.ops.logFilters.confirmEnable') }}</button>
            <button type="button" class="btn btn-secondary" @click="editor = null">{{ translate('common.cancel') }}</button>
          </div>
          <p class="text-xs text-gray-500">{{ translate('admin.ops.logFilters.confirmSaves') }}</p>
        </section>
      </fieldset>
      <details v-if="!loading" class="rounded-xl border border-gray-200 p-3 dark:border-dark-700">
        <summary class="cursor-pointer font-medium">{{ translate('admin.ops.logFilters.legacy') }}</summary>
        <p class="my-3 text-xs text-gray-500">{{ translate('admin.ops.logFilters.legacyHint') }}</p>
        <p v-if="legacyError" role="alert" class="text-red-600 dark:text-red-400">{{ legacyError }}</p>
        <button v-if="!legacy && legacyError" type="button" class="btn btn-secondary mt-2" :disabled="busy" @click="load(false)">{{ translate('admin.ops.logFilters.reload') }}</button>
        <fieldset v-if="legacy" :disabled="busy" class="space-y-3">
          <div v-for="field in legacyLogFilters" :key="field.key" class="flex items-center justify-between gap-4">
            <div>
              <p>{{ translate(`admin.ops.settings.${field.label}`) }}</p>
              <p class="mt-1 text-xs text-gray-500">{{ translate(`admin.ops.settings.${field.label}Hint`) }}</p>
            </div>
            <Toggle v-model="legacy[field.key]" :aria-label="translate(`admin.ops.settings.${field.label}`)" :data-testid="field.key" />
          </div>
          <button type="button" class="btn btn-secondary" data-testid="save-legacy" @click="saveLegacy">{{ translate('admin.ops.logFilters.saveLegacy') }}</button>
        </fieldset>
      </details>
    </div>
    <template #footer>
      <button type="button" class="btn btn-secondary" :disabled="saving || legacySaving" @click="close">{{ translate('common.close') }}</button>
      <button type="button" class="btn btn-primary" data-testid="save-rules" :disabled="!config || !hasPendingRules || busy || conflict || !!editor" @click="saveRules">{{ saving ? translate('common.saving') : translate('common.save') }}</button>
    </template>
  </BaseDialog>
</template>
