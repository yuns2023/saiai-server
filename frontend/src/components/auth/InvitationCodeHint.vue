<script setup lang="ts">
import { computed, onMounted } from 'vue'
import { useAppStore } from '@/stores'
import { linkify } from '@/utils/linkify'

// Admin-set "how to get an invitation code" text (Settings → Registration).
// Rendered as text nodes; only http(s) URLs become links. Reads the store's
// cached settings reactively: fetchPublicSettings() returns null while another
// request is in flight, so its return value alone could miss the text.
const appStore = useAppStore()
const segments = computed(() => linkify(appStore.cachedPublicSettings?.invitation_code_hint?.trim() || ''))

onMounted(() => {
  void appStore.fetchPublicSettings()
})
</script>

<template>
  <p v-if="segments.length" class="input-hint whitespace-pre-line" data-testid="invitation-code-hint">
    <template v-for="(seg, i) in segments" :key="i">
      <a
        v-if="seg.href"
        :href="seg.href"
        target="_blank"
        rel="noopener noreferrer"
        class="text-primary-600 hover:underline dark:text-primary-400"
        >{{ seg.text }}</a
      >
      <template v-else>{{ seg.text }}</template>
    </template>
  </p>
</template>
