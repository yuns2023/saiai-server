<script setup lang="ts">
import UserIdentityTooltip from '@/components/admin/user/UserIdentityTooltip.vue'
import { ref } from 'vue'
import { useI18n } from 'vue-i18n'
import { adminAPI } from '@/api/admin'
import type { AdminUser } from '@/types'
const props = defineProps<{ modelValue: number[] }>()
const emit = defineEmits<{ (e: 'update:modelValue', ids: number[]): void }>()
const { t } = useI18n()
const search = ref('')
const users = ref<AdminUser[]>([])
const error = ref(false)
const loading = ref(false)
async function find() {
  loading.value = true
  error.value = false
  try { users.value = (await adminAPI.users.list(1, 20, { search: search.value })).items }
  catch { error.value = true }
  finally { loading.value = false }
}
function toggle(id: number) {
  emit('update:modelValue', props.modelValue.includes(id) ? props.modelValue.filter(v => v !== id) : [...props.modelValue, id])
}
</script>
<template>
  <div class="space-y-2">
    <div class="flex gap-2">
      <input v-model="search" class="input min-w-0 flex-1" :placeholder="t('admin.users.searchUsers')" @keydown.enter.prevent="find" />
      <button type="button" class="btn btn-secondary whitespace-nowrap" :disabled="loading" @click="find">{{ t('common.search') }}</button>
    </div>
    <p v-if="error" role="alert">{{ t('admin.accounts.carpoolUserSearchFailed') }}</p>
    <div class="flex flex-wrap gap-2">
      <button v-for="id in modelValue" :key="id" type="button" class="btn btn-secondary btn-sm" @click="toggle(id)">{{ t('admin.accounts.carpoolSelectedUser', { id }) }} ×</button>
    </div>
    <label v-for="user in users" :key="user.id" class="flex items-center gap-2 text-sm">
      <input type="checkbox" class="h-4 w-4 shrink-0 rounded-sm border-gray-300 text-primary-600 focus:ring-primary-500" :checked="modelValue.includes(user.id)" @change="toggle(user.id)" />
      <UserIdentityTooltip :user-id="user.id" :username="user.username" :notes="user.notes" :pin-on-click="false"><span>{{ user.email }}</span></UserIdentityTooltip> (#{{ user.id }})
    </label>
  </div>
</template>
