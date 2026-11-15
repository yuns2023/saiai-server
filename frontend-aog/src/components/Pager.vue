<script setup lang="ts">
const props = defineProps<{ page: number; pages: number; total: number; busy?: boolean }>()
const emit = defineEmits<{ go: [page: number] }>()
const go = (p: number) => {
  if (p >= 1 && p <= props.pages && p !== props.page) emit('go', p)
}
</script>

<template>
  <nav v-if="pages > 1" class="flex items-center justify-between gap-3 pt-4 text-sm text-ink-2" :aria-label="$t('common.pagination')">
    <span class="num">{{ $t('common.pageOf', { page, pages, total }) }}</span>
    <div class="flex gap-2">
      <button type="button" class="btn btn-line btn-sm" :disabled="busy || page <= 1" @click="go(page - 1)">{{ $t('common.prev') }}</button>
      <button type="button" class="btn btn-line btn-sm" :disabled="busy || page >= pages" @click="go(page + 1)">{{ $t('common.next') }}</button>
    </div>
  </nav>
</template>
