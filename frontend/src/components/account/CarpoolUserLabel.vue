<script setup lang="ts">
import { ref, watch } from 'vue'
import UserIdentityTooltip from '@/components/admin/user/UserIdentityTooltip.vue'
import { loadUserIdentity, type UserIdentity } from '@/components/admin/user/userIdentityCache'

/**
 * Shows a carpool allowlist user by email (username and notes on hover). Saved
 * allowlists only carry IDs, so the identity is fetched once per user through the
 * shared cache; the bare #id stays visible until it loads or if it fails.
 */
const props = defineProps<{ userId: number }>()
const identity = ref<UserIdentity | null>(null)

watch(() => props.userId, async (id) => {
  identity.value = null
  try {
    const value = await loadUserIdentity(id)
    if (props.userId === id) identity.value = value
  } catch {
    // Keep the #id fallback; hovering retries through the tooltip.
  }
}, { immediate: true })
</script>
<template>
  <UserIdentityTooltip :user-id="userId" :username="identity?.username" :notes="identity?.notes" :pin-on-click="false">
    <span data-testid="carpool-user-label">{{ identity?.email || `#${userId}` }}</span>
  </UserIdentityTooltip>
</template>
