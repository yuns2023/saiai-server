<script setup lang="ts">
import { nextTick, onMounted } from 'vue'
import { useRoute } from 'vue-router'
import { useI18n } from 'vue-i18n'
import { intlLocale } from '@/i18n'
import { dateTime } from '@/lib/format'
import { renderMarkdown } from '@/lib/markdown'
import { useNoticesStore } from '@/stores/notices'

const { t } = useI18n()
const route = useRoute()
const notices = useNoticesStore()

onMounted(async () => {
  await notices.load().catch(() => undefined)
  await nextTick()
  const anchor = /^#n\d+$/.test(route.hash) ? route.hash.slice(1) : ''
  if (anchor) document.getElementById(anchor)?.scrollIntoView({ block: 'start' })
  // Opening the page counts as reading what is on it.
  for (const n of notices.unread) notices.markRead(n.id)
})
</script>

<template>
  <section aria-labelledby="notices-page-title" class="max-w-3xl">
    <h1 id="notices-page-title" class="text-lg font-semibold tracking-tight">{{ t('nav.notices') }}</h1>
    <div v-if="!notices.loaded" class="mt-6 space-y-3"><div v-for="i in 3" :key="i" class="skeleton h-16" /></div>
    <p v-else-if="!notices.items.length" class="mt-6 border-t border-rule-strong py-8 text-sm text-ink-2">{{ t('notices.empty') }}</p>
    <ol v-else class="mt-5 border-t border-rule-strong">
      <li v-for="n in notices.items" :id="`n${n.id}`" :key="n.id" class="scroll-mt-6 border-b border-rule py-5">
        <div class="flex flex-wrap items-baseline justify-between gap-x-4 gap-y-1">
          <h2 class="text-md font-semibold">{{ n.title }}</h2>
          <time class="num text-xs text-ink-3" :datetime="n.starts_at || n.created_at">{{ dateTime(n.starts_at || n.created_at, intlLocale()) }}</time>
        </div>
        <!-- eslint-disable-next-line vue/no-v-html -- sanitized with DOMPurify -->
        <div class="prose-notice mt-2 text-sm leading-relaxed text-ink-2" v-html="renderMarkdown(n.content)" />
      </li>
    </ol>
  </section>
</template>

<style scoped>
.prose-notice :deep(p) {
  margin: 0.5em 0;
  max-width: 70ch;
}
.prose-notice :deep(a) {
  color: var(--blue);
}
.prose-notice :deep(ul),
.prose-notice :deep(ol) {
  padding-left: 1.25em;
  list-style: revert;
}
.prose-notice :deep(code) {
  font-family: var(--font-mono);
  font-size: 0.92em;
  background: var(--shell);
  padding: 0 0.25em;
  border-radius: 3px;
}
.prose-notice :deep(img) {
  max-width: 100%;
}
</style>
