<template>
  <fieldset class="space-y-3 rounded-lg border border-gray-200 p-4 dark:border-dark-600">
    <label class="flex items-center gap-2 font-medium">
      <input type="checkbox" :checked="modelValue.enabled" :disabled="!supported" @change="setEnabled" />
      {{ t('keys.smartSwitch.title') }}
    </label>
    <p class="input-hint">{{ t('keys.smartSwitch.hint') }}</p>
    <p v-if="!supported" class="input-hint">{{ t('keys.smartSwitch.unsupported') }}</p>
    <template v-if="modelValue.enabled">
      <div v-for="group in available" :key="group.id" class="flex items-center gap-2">
        <input :id="`smart-group-${group.id}`" type="checkbox" :checked="modelValue.group_ids.includes(group.id)" @change="toggle(group.id)" />
        <label :for="`smart-group-${group.id}`">
          {{ group.name }} · {{ group.subscription_type === 'subscription' ? t('keys.smartSwitch.subscription') : t('keys.smartSwitch.payg') }}
          · ×{{ group.rate_multiplier }}
        </label>
      </div>
      <ol class="space-y-2">
        <li v-for="(id, index) in modelValue.group_ids" :key="id" class="flex items-center gap-2">
          <span class="flex-1">{{ index + 1 }}. {{ groups.find(group => group.id === id)?.name ?? `${id} (${t('keys.smartSwitch.unavailable')})` }}</span>
          <button type="button" class="btn btn-secondary" :disabled="index === 0" :aria-label="t('keys.smartSwitch.up')" @click="move(index, -1)">↑</button>
          <button type="button" class="btn btn-secondary" :disabled="index === modelValue.group_ids.length - 1" :aria-label="t('keys.smartSwitch.down')" @click="move(index, 1)">↓</button>
          <button type="button" class="btn btn-secondary" :aria-label="t('keys.smartSwitch.remove')" @click="toggle(id)">×</button>
        </li>
      </ol>
      <label class="flex items-start gap-2 text-sm">
        <input type="checkbox" :checked="modelValue.payg_confirmed" @change="setConfirmed" />
        {{ t('keys.smartSwitch.consent') }}
      </label>
      <label class="block text-sm">{{ t('keys.smartSwitch.budget') }}
        <input type="number" min="0" step="0.01" class="input mt-1" :value="modelValue.payg_budget_usd ?? ''" @input="setBudget" />
      </label>
      <p class="input-hint">{{ t('keys.smartSwitch.budgetHint', { used: (used ?? 0).toFixed(4) }) }}</p>
      <p class="input-hint">{{ t('keys.smartSwitch.scope') }}</p>
    </template>
  </fieldset>
</template>

<script setup lang="ts">
import { computed } from 'vue'
import { useI18n } from 'vue-i18n'
import type { Group, KeySmartSwitchPolicy } from '@/types'
const props = defineProps<{ modelValue: KeySmartSwitchPolicy; groups: Group[]; groupId: number | null; used?: number }>()
const emit = defineEmits<{ 'update:modelValue': [value: KeySmartSwitchPolicy] }>()
const { t } = useI18n()
const base = computed(() => props.groups.find(group => group.id === props.groupId))
const supported = computed(() => !!base.value && ['anthropic', 'openai'].includes(base.value.platform) && !base.value.hosted_usage_enabled)
const available = computed(() => props.groups.filter(group => supported.value && group.platform === base.value?.platform && !group.hosted_usage_enabled))
function setEnabled(event: Event) {
  emit('update:modelValue', { ...props.modelValue, enabled: (event.target as HTMLInputElement).checked })
}
function setConfirmed(event: Event) {
  emit('update:modelValue', { ...props.modelValue, payg_confirmed: (event.target as HTMLInputElement).checked })
}
function toggle(id: number) {
  const ids = props.modelValue.group_ids.includes(id)
    ? props.modelValue.group_ids.filter(value => value !== id)
    : [...props.modelValue.group_ids, id]
  emit('update:modelValue', { ...props.modelValue, group_ids: ids })
}
function move(index: number, offset: number) {
  const ids = [...props.modelValue.group_ids]
  const target = index + offset
  if (target < 0 || target >= ids.length) return
  ;[ids[index], ids[target]] = [ids[target], ids[index]]
  emit('update:modelValue', { ...props.modelValue, group_ids: ids })
}
function setBudget(event: Event) {
  const value = (event.target as HTMLInputElement).value
  emit('update:modelValue', { ...props.modelValue, payg_budget_usd: value === '' ? null : Number(value) })
}
</script>
