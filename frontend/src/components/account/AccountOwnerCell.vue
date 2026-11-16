<script setup lang="ts">
// 号主列：邮箱 + 灰色用户 ID；未设置号主时显示 "-"（与表格其他空值一致）。
defineProps<{ ownerUserId: number | null | undefined; ownerEmail: string | null | undefined }>()
</script>
<template>
  <div v-if="ownerUserId" class="flex flex-col whitespace-normal [overflow-wrap:anywhere]" data-testid="account-owner">
    <span v-if="ownerEmail" class="text-sm text-gray-700 dark:text-gray-300" :title="ownerEmail">{{ ownerEmail }}</span>
    <span class="text-xs text-gray-400 dark:text-dark-500">#{{ ownerUserId }}</span>
  </div>
  <span v-else class="text-sm text-gray-400 dark:text-dark-500" data-testid="account-owner-empty">-</span>
</template>
