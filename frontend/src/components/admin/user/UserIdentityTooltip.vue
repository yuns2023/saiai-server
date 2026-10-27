<script setup lang="ts">
import { computed, onBeforeUnmount, ref, watch } from 'vue'
import { useI18n } from 'vue-i18n'
import HelpTooltip from '@/components/common/HelpTooltip.vue'
import { loadUserIdentity, type UserIdentity } from './userIdentityCache'

/**
 * Wraps a displayed user email and shows the user's username and admin notes on hover.
 * When the caller already has both fields they are shown directly; otherwise they are
 * fetched once per user on first hover and cached.
 */
const props = withDefaults(defineProps<{
  userId?: number | null
  username?: string | null
  notes?: string | null
  /** Extra line under the identity, e.g. what clicking the trigger does. */
  hint?: string
  /** Set false when the wrapped trigger opens something on click. */
  pinOnClick?: boolean
}>(), { userId: null, username: undefined, notes: undefined, hint: '', pinOnClick: true })

const { t } = useI18n()

// Keyed by user so a reused row never shows (or waits on) the previous user's details.
const fetched = ref<{ userId: number; identity: UserIdentity | null; failed: boolean } | null>(null)

const known = computed(() => props.username !== undefined && props.notes !== undefined)
const current = computed(() => (fetched.value && fetched.value.userId === props.userId ? fetched.value : null))
const identity = computed<UserIdentity | null>(() =>
  known.value ? { username: props.username ?? '', notes: props.notes ?? '' } : current.value?.identity ?? null,
)
const failed = computed(() => !known.value && !!current.value?.failed)

// Sweeping the pointer across a list should not fetch every user it passes over.
const HOVER_DELAY_MS = 200
let hoverTimer: ReturnType<typeof setTimeout> | null = null
let engaged = false

function cancelHover() {
  if (hoverTimer) clearTimeout(hoverTimer)
  hoverTimer = null
}

function onEnter() {
  engaged = true
  cancelHover()
  hoverTimer = setTimeout(load, HOVER_DELAY_MS)
}

function onLeave() {
  engaged = false
  cancelHover()
}

function onFocus() {
  engaged = true
  load()
}

watch(() => props.userId, () => {
  if (engaged) load()
})

onBeforeUnmount(cancelHover)

// Every hover goes through the cache, which dedupes in-flight requests and applies the TTL.
async function load() {
  cancelHover()
  const userId = props.userId
  if (known.value || !userId) return
  if (fetched.value?.userId !== userId || fetched.value.failed) fetched.value = { userId, identity: null, failed: false }
  try {
    const value = await loadUserIdentity(userId)
    if (props.userId === userId) fetched.value = { userId, identity: value, failed: false }
  } catch {
    if (props.userId === userId) fetched.value = { userId, identity: null, failed: true }
  }
}
</script>

<template>
  <span v-if="!userId && !known" class="inline"><slot /></span>
  <span v-else class="inline-flex min-w-0 max-w-full" @mouseenter="onEnter" @mouseleave="onLeave" @focusin="onFocus" @focusout="onLeave">
    <HelpTooltip lazy :pin-on-click="pinOnClick" class="!ml-0 min-w-0 max-w-full">
      <template #trigger><slot /></template>
      <div v-if="identity" class="space-y-1" data-testid="user-identity-tooltip">
        <div>
          <span class="text-gray-400">{{ t('admin.users.identityTooltip.username') }}: </span>
          <span class="break-all">{{ identity.username || t('admin.users.identityTooltip.empty') }}</span>
        </div>
        <div>
          <span class="text-gray-400">{{ t('admin.users.identityTooltip.notes') }}: </span>
          <span class="whitespace-pre-wrap break-words">{{ identity.notes || t('admin.users.identityTooltip.empty') }}</span>
        </div>
      </div>
      <div v-else-if="failed">{{ t('admin.users.identityTooltip.loadFailed') }}</div>
      <div v-else>{{ t('admin.users.identityTooltip.loading') }}</div>
      <div v-if="userId || hint" class="mt-1 border-t border-white/10 pt-1 text-gray-400">
        <span v-if="userId">#{{ userId }}</span>
        <span v-if="userId && hint"> · </span>
        <span v-if="hint">{{ hint }}</span>
      </div>
    </HelpTooltip>
  </span>
</template>
