<script setup lang="ts">
import { computed } from 'vue'
import { useAppStore } from '@/stores/app'
import { linkify } from '@/lib/linkify'

const app = useAppStore()
const segments = computed(() => linkify(app.invitationCodeHint))
</script>

<template>
  <span v-if="segments.length" class="mt-1 block whitespace-pre-line text-xs text-ink-3" data-testid="invite-hint">
    <template v-for="(seg, i) in segments" :key="i">
      <a v-if="seg.href" :href="seg.href" target="_blank" rel="noopener noreferrer">{{ seg.text }}</a>
      <template v-else>{{ seg.text }}</template>
    </template>
  </span>
</template>
