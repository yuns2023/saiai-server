<template>
  <div class="space-y-3" data-testid="moderation-policy-editor">
    <p v-if="loadFailed" role="alert">{{ t('admin.groups.inputModeration.policy.loadFailed') }} <button type="button" class="btn btn-secondary" @click="load">{{ t('admin.settings.jev.retry') }}</button></p>
    <template v-else-if="catalog">
      <label class="input-label">{{ t('admin.groups.inputModeration.policy.version') }}
        <select :value="version" class="input mt-1" @change="changeVersion(($event.target as HTMLSelectElement).value)">
          <option :value="catalog.legacy_version">{{ t('admin.groups.inputModeration.policy.legacy') }}</option>
          <option :value="catalog.expanded_version">{{ t('admin.groups.inputModeration.policy.expanded') }}</option>
        </select>
      </label>
      <p class="input-hint">{{ t('admin.groups.inputModeration.policy.hint') }}</p>
      <p v-if="unsupportedLegacy.length" class="text-sm text-amber-700">{{ t('admin.groups.inputModeration.policy.unsupported') }} {{ unsupportedLegacy.join(', ') }}</p>
      <div class="overflow-x-auto">
        <table class="w-full text-left text-sm">
          <thead><tr><th>{{ t('admin.groups.inputModeration.policy.category') }}</th><th>{{ t('admin.groups.inputModeration.policy.audit') }}</th><th>{{ t('admin.groups.inputModeration.policy.enforce') }}</th><th>{{ t('admin.groups.inputModeration.policy.threshold') }}</th></tr></thead>
          <tbody>
            <tr v-for="category in visibleCategories" :key="category.id" :data-category="category.id">
              <td class="py-2 pr-2">{{ locale.startsWith('zh') ? category.label_zh : category.label_en }}<span class="block max-w-xs text-xs text-gray-500">{{ locale.startsWith('zh') ? category.description_zh : category.description_en }}</span><span v-if="!category.enforcement_allowed" class="block text-xs text-gray-500">{{ t('admin.groups.inputModeration.policy.auditOnly') }}</span></td>
              <td><input type="checkbox" :aria-label="category.id + ' audit'" :checked="audited(category.id)" @change="toggleAudit(category.id)" /></td>
              <td><input type="checkbox" :aria-label="category.id + ' enforcement'" :checked="enforced(category.id)" :disabled="!category.enforcement_allowed || !audited(category.id)" @change="toggleEnforcement(category.id)" /></td>
              <td><input type="number" min="1" max="100" :aria-label="category.id + ' threshold'" class="input w-20" :value="modelValue?.thresholds?.[category.id] ?? minConfidence" :disabled="!audited(category.id)" @change="setThreshold(category.id, ($event.target as HTMLInputElement).value)" /></td>
            </tr>
          </tbody>
        </table>
      </div>
      <p class="input-hint">{{ t('admin.groups.inputModeration.policy.enforcementHint') }}</p>
    </template>
    <p v-else role="status">{{ t('common.loading') }}</p>
  </div>
</template>
<script setup lang="ts">
import { computed, onMounted, ref } from 'vue'
import { useI18n } from 'vue-i18n'
import { getJevCategories } from '@/api/admin/settings'
import type { InputModerationPolicy } from '@/types'
const props = defineProps<{ modelValue?: InputModerationPolicy | null; legacyCategories: string[]; minConfidence: number }>()
const emit = defineEmits<{ 'update:modelValue': [value: InputModerationPolicy] }>()
const { t, locale } = useI18n()
const catalog = ref<Awaited<ReturnType<typeof getJevCategories>>>()
const loadFailed = ref(false)
const version = computed(() => props.modelValue?.rules_version ?? catalog.value?.legacy_version)
const visibleCategories = computed(() => catalog.value?.categories.filter(c => version.value === catalog.value?.expanded_version || c.legacy) ?? [])
const unsupportedLegacy = computed(() => props.modelValue ? [] : props.legacyCategories.filter(id => !catalog.value?.categories.some(c => c.legacy && c.id.toLowerCase() === id.toLowerCase())))
function audited(id: string) { return props.modelValue ? (props.modelValue.audit_categories ?? []).includes(id) : !!catalog.value?.categories.find(c => c.id === id)?.legacy }
function enforced(id: string) { return props.modelValue ? (props.modelValue.enforcement_categories ?? []).includes(id) : (props.legacyCategories.length === 0 || props.legacyCategories.some(c => c.toLowerCase() === id.toLowerCase())) }
function current(): InputModerationPolicy {
  if (props.modelValue) return { ...JSON.parse(JSON.stringify(props.modelValue)), audit_categories: [...(props.modelValue.audit_categories ?? [])], enforcement_categories: [...(props.modelValue.enforcement_categories ?? [])], thresholds: { ...(props.modelValue.thresholds ?? {}) } }
  const legacy = catalog.value?.categories.filter(c => c.legacy).map(c => c.id) ?? []
  return { rules_version: 'jev-v2', audit_categories: legacy, enforcement_categories: legacy.filter(enforced), thresholds: {} }
}
function changeVersion(value: string) {
  if (!catalog.value) return
  const p = current()
  p.rules_version = value === catalog.value.expanded_version ? catalog.value.expanded_version : catalog.value.legacy_version
  p.audit_categories = catalog.value.categories.filter(c => p.rules_version === catalog.value?.expanded_version || c.legacy).map(c => c.id)
  p.enforcement_categories = p.enforcement_categories.filter(id => p.audit_categories.includes(id))
  p.thresholds = Object.fromEntries(Object.entries(p.thresholds).filter(([id]) => p.audit_categories.includes(id)))
  emit('update:modelValue', p)
}
function toggleAudit(id: string) {
  const p = current()
  p.audit_categories = p.audit_categories.includes(id) ? p.audit_categories.filter(c => c !== id) : [...p.audit_categories, id]
  p.enforcement_categories = p.enforcement_categories.filter(c => p.audit_categories.includes(c))
  if (!p.audit_categories.includes(id)) delete p.thresholds[id]
  emit('update:modelValue', p)
}
function toggleEnforcement(id: string) {
  if (!audited(id) || !catalog.value?.categories.find(c => c.id === id)?.enforcement_allowed) return
  const p = current()
  p.enforcement_categories = p.enforcement_categories.includes(id) ? p.enforcement_categories.filter(c => c !== id) : [...p.enforcement_categories, id]
  emit('update:modelValue', p)
}
function setThreshold(id: string, raw: string) {
  const value = Number(raw)
  if (!Number.isInteger(value) || value < 1 || value > 100) return
  const p = current(); p.thresholds[id] = value; emit('update:modelValue', p)
}
async function load() {
  loadFailed.value = false
  try { catalog.value = await getJevCategories() } catch { loadFailed.value = true }
}
onMounted(load)
</script>
