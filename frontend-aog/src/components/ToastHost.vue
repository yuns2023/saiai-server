<script setup lang="ts">
import { useAppStore } from '@/stores/app'
import Icon from './Icon.vue'

const app = useAppStore()
</script>

<template>
  <div class="pointer-events-none fixed inset-x-0 top-3 z-50 flex flex-col items-center gap-2 px-3" aria-live="polite">
    <TransitionGroup name="toast">
      <div
        v-for="t in app.toasts"
        :key="t.id"
        role="status"
        class="pointer-events-auto flex max-w-md items-start gap-2 rounded border bg-paper px-3 py-2 text-sm shadow-[0_6px_20px_-8px_rgb(20_32_58/0.35)]"
        :class="t.kind === 'error' ? 'border-seal text-seal' : t.kind === 'ok' ? 'border-ok text-ink' : 'border-rule text-ink'"
      >
        <Icon v-if="t.kind === 'ok'" name="check" class="mt-0.5 text-ok" />
        <span class="min-w-0 break-words">{{ t.text }}</span>
        <button type="button" class="ml-1 mt-0.5 text-ink-3 hover:text-ink" :aria-label="$t('common.close')" @click="app.dismiss(t.id)">
          <Icon name="close" :size="14" />
        </button>
      </div>
    </TransitionGroup>
  </div>
</template>

<style scoped>
.toast-enter-active,
.toast-leave-active {
  transition:
    opacity 180ms ease-out,
    transform 180ms cubic-bezier(0.16, 1, 0.3, 1);
}
.toast-enter-from,
.toast-leave-to {
  opacity: 0;
  transform: translateY(-6px);
}
</style>
