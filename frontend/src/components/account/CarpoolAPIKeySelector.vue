<script setup lang="ts">
import { ref } from 'vue'
import { useI18n } from 'vue-i18n'
import { adminAPI } from '@/api/admin'
import type { CarpoolAPIKeyRef } from '@/types'
const props = defineProps<{ modelValue: CarpoolAPIKeyRef[] }>()
const emit = defineEmits<{ (e: 'update:modelValue', keys: CarpoolAPIKeyRef[]): void }>()
const { t } = useI18n()
const search = ref('')
const keys = ref<CarpoolAPIKeyRef[]>([])
const error = ref(false)
const loading = ref(false)
// A bare number lists that user's keys; anything else matches key names.
async function find() {
  if (loading.value) return
  loading.value = true
  error.value = false
  const q = search.value.trim()
  try { keys.value = /^\d+$/.test(q) ? await adminAPI.usage.searchApiKeys(Number(q)) : await adminAPI.usage.searchApiKeys(undefined, q) }
  catch { error.value = true }
  finally { loading.value = false }
}
function selected(id: number) {
  return props.modelValue.some(k => k.id === id)
}
function toggle(key: CarpoolAPIKeyRef) {
  emit('update:modelValue', selected(key.id) ? props.modelValue.filter(k => k.id !== key.id) : [...props.modelValue, key])
}
</script>
<template>
  <div class="space-y-2">
    <div class="flex gap-2">
      <input v-model="search" class="input min-w-0 flex-1" :placeholder="t('admin.accounts.carpoolAPIKeySearchPlaceholder')" @keydown.enter.prevent="find" />
      <button type="button" class="btn btn-secondary whitespace-nowrap" :disabled="loading" @click="find">{{ t('common.search') }}</button>
    </div>
    <p v-if="error" role="alert">{{ t('admin.accounts.carpoolAPIKeySearchFailed') }}</p>
    <div class="flex flex-wrap gap-2">
      <button v-for="key in modelValue" :key="key.id" type="button" class="btn btn-secondary btn-sm" @click="toggle(key)">{{ t('admin.accounts.carpoolSelectedAPIKey', { name: key.name, id: key.id, userId: key.user_id }) }} ×</button>
    </div>
    <label v-for="key in keys" :key="key.id" class="flex items-center gap-2 text-sm">
      <input type="checkbox" class="h-4 w-4 shrink-0 rounded-sm border-gray-300 text-primary-600 focus:ring-primary-500" :checked="selected(key.id)" @change="toggle(key)" />
      <span>{{ t('admin.accounts.carpoolSelectedAPIKey', { name: key.name, id: key.id, userId: key.user_id }) }}</span>
    </label>
  </div>
</template>
