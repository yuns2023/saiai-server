<template>
  <div class="flex flex-wrap items-center gap-3">
    <SearchInput
      :model-value="searchQuery"
      :placeholder="t('admin.accounts.searchAccounts')"
      class="w-full sm:w-64"
      @update:model-value="$emit('update:searchQuery', $event)"
      @search="$emit('change')"
    />
    <Select :model-value="filters.platform" class="w-40" :options="pOpts" @update:model-value="updatePlatform" @change="$emit('change')" />
    <Select :model-value="filters.type" class="w-40" :options="tOpts" @update:model-value="updateType" @change="$emit('change')" />
    <Select :model-value="filters.status" class="w-40" :options="sOpts" @update:model-value="updateStatus" @change="$emit('change')" />
    <Select :model-value="filters.group" class="w-40" :options="gOpts" @update:model-value="updateGroup" @change="$emit('change')" />
    <input
      v-model="ownerText"
      type="text"
      inputmode="numeric"
      autocomplete="off"
      :class="['input w-40', ownerInvalid && 'input-error']"
      :aria-invalid="ownerInvalid"
      data-testid="owner-filter"
      :placeholder="t('admin.accounts.owner.filterPlaceholder')"
      :title="ownerInvalid ? t('admin.accounts.owner.filterInvalid') : t('admin.accounts.owner.filterHint')"
      @input="updateOwner(($event.target as HTMLInputElement).value)"
    />
  </div>
</template>

<script setup lang="ts">
import { computed, ref, watch } from 'vue'; import { useI18n } from 'vue-i18n'; import Select from '@/components/common/Select.vue'; import SearchInput from '@/components/common/SearchInput.vue'
import type { AdminGroup } from '@/types'
const props = defineProps<{ searchQuery: string; filters: Record<string, any>; groups?: AdminGroup[] }>()
const emit = defineEmits(['update:searchQuery', 'update:filters', 'change']); const { t } = useI18n()
const updatePlatform = (value: string | number | boolean | null) => { emit('update:filters', { ...props.filters, platform: value }) }
const updateType = (value: string | number | boolean | null) => { emit('update:filters', { ...props.filters, type: value }) }
const updateStatus = (value: string | number | boolean | null) => { emit('update:filters', { ...props.filters, status: value }) }
const updateGroup = (value: string | number | boolean | null) => { emit('update:filters', { ...props.filters, group: value }) }
// 号主筛选：输入正整数用户 ID 才生效；清空则取消筛选（不发送参数）；
// 非法输入只标红，保留当前筛选和输入文本，不触发重新加载。
const parseOwnerId = (raw: string): number | undefined | null => {
  const text = raw.trim()
  if (!text) return undefined
  const id = /^\d+$/.test(text) ? Number(text) : 0
  return Number.isSafeInteger(id) && id > 0 ? id : null
}
const ownerText = ref(props.filters.owner_user_id ? String(props.filters.owner_user_id) : '')
const ownerInvalid = computed(() => parseOwnerId(ownerText.value) === null)
watch(() => props.filters.owner_user_id, (id) => {
  const parsed = parseOwnerId(ownerText.value)
  if (parsed !== null && parsed !== id) ownerText.value = id ? String(id) : ''
})
const updateOwner = (raw: string) => {
  const next = parseOwnerId(raw)
  if (next === null || next === props.filters.owner_user_id) return
  emit('update:filters', { ...props.filters, owner_user_id: next })
  emit('change')
}
const pOpts = computed(() => [{ value: '', label: t('admin.accounts.allPlatforms') }, { value: 'anthropic', label: 'Anthropic' }, { value: 'openai', label: 'OpenAI' }, { value: 'gemini', label: 'Gemini' }, { value: 'antigravity', label: 'Antigravity' }, { value: 'sora', label: 'Sora' }])
const tOpts = computed(() => [{ value: '', label: t('admin.accounts.allTypes') }, { value: 'oauth', label: t('admin.accounts.oauthType') }, { value: 'setup-token', label: t('admin.accounts.setupToken') }, { value: 'apikey', label: t('admin.accounts.apiKey') }, { value: 'bedrock', label: 'AWS Bedrock' }])
const sOpts = computed(() => [{ value: '', label: t('admin.accounts.allStatus') }, { value: 'active', label: t('admin.accounts.status.active') }, { value: 'inactive', label: t('admin.accounts.status.inactive') }, { value: 'error', label: t('admin.accounts.status.error') }, { value: 'rate_limited', label: t('admin.accounts.status.rateLimited') }, { value: 'temp_unschedulable', label: t('admin.accounts.status.tempUnschedulable') }])
const gOpts = computed(() => [
  { value: '', label: t('admin.accounts.allGroups') },
  { value: 'ungrouped', label: t('admin.accounts.ungroupedGroup') },
  ...(props.groups || []).map(g => ({ value: String(g.id), label: g.name }))
])
</script>
