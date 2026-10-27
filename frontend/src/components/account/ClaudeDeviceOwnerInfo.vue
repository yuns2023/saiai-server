<template>
  <div class="break-all text-xs text-gray-500 dark:text-gray-400">
    {{ t('admin.users.claudeDeviceModal.lastUser') }}:
    <UserIdentityTooltip v-if="owner" :user-id="owner.user_id" :username="owner.username">
      <span>{{ `${owner.username || owner.email || '—'} (#${owner.user_id})` }}</span>
    </UserIdentityTooltip>
    <template v-else>—</template>
  </div>
  <div class="break-all text-xs text-gray-500 dark:text-gray-400">
    {{ t('admin.users.claudeDeviceModal.lastKey') }}: {{ owner ? `${owner.api_key_name || '—'} (#${owner.api_key_id})` : '—' }}
  </div>
</template>

<script setup lang="ts">
import UserIdentityTooltip from '@/components/admin/user/UserIdentityTooltip.vue'
import { useI18n } from 'vue-i18n'
import type { ClaudeDeviceOwner } from '@/api/admin/accounts'

defineProps<{ owner?: ClaudeDeviceOwner }>()
const { t } = useI18n()
</script>
