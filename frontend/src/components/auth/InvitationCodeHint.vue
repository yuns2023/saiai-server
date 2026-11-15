<script setup lang="ts">
import { computed, onMounted, ref } from 'vue'
import { useAppStore } from '@/stores'
import { linkify } from '@/utils/linkify'

// Admin-set "how to get an invitation code" text (Settings → Registration).
// Rendered as text nodes; only http(s) URLs become links.
const appStore = useAppStore()
const text = ref('')
const segments = computed(() => linkify(text.value))

onMounted(async () => {
  const settings = await appStore.fetchPublicSettings()
  text.value = settings?.invitation_code_hint?.trim() || ''
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
