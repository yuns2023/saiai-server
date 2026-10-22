<template>
  <div
    class="table-page-layout"
    :class="{ 'mobile-mode': isMobile, 'table-page-layout--dense': dense }"
    data-testid="table-page-layout"
  >
    <!-- 固定区域：操作按钮 -->
    <div v-if="$slots.actions" class="layout-section-fixed">
      <slot name="actions" />
    </div>

    <!-- 固定区域：搜索和过滤器 -->
    <div v-if="$slots.filters" class="layout-section-fixed">
      <slot name="filters" />
    </div>

    <!-- 滚动区域：表格 -->
    <div class="layout-section-scrollable">
      <div class="card table-scroll-container">
        <slot name="table" />
      </div>
    </div>

    <!-- 固定区域：分页器 -->
    <div v-if="$slots.pagination" class="layout-section-fixed">
      <slot name="pagination" />
    </div>
  </div>
</template>

<script setup lang="ts">
import { ref, onMounted, onUnmounted } from 'vue'

withDefaults(
  defineProps<{
    /** Compact gaps and table cell padding for data-heavy pages. */
    dense?: boolean
  }>(),
  { dense: false }
)

const isMobile = ref(false)

const checkMobile = () => {
  isMobile.value = window.innerWidth < 1024
}

onMounted(() => {
  checkMobile()
  window.addEventListener('resize', checkMobile)
})

onUnmounted(() => {
  window.removeEventListener('resize', checkMobile)
})
</script>

<style scoped>
@reference "../../style.css";

/*
 * 桌面端：Flexbox 布局
 * 页面至少占满视口；表格区域占据剩余高度并在内部滚动（表头 sticky）。
 * 固定区域（统计/筛选/分页）过高时表格不再被压扁：表格区域保底
 * --table-page-min-height，页面整体自然滚动。
 */
.table-page-layout {
  @apply flex flex-col gap-6;
  --table-page-min-height: max(360px, 50vh);
  min-height: calc(100vh - 64px - 4rem); /* 减去 header + lg:p-8 的上下padding */
}

.table-page-layout--dense {
  @apply gap-3;
}

.layout-section-fixed {
  @apply flex-shrink-0;
}

.layout-section-scrollable {
  @apply flex flex-col;
  /* 0px（而非 0%）基准：列方向容器高度不定时，表格行数不会撑高本区域 */
  flex: 1 1 0px;
  min-height: var(--table-page-min-height);
}

/* 表格滚动容器 - 增强版表体滚动方案 */
.table-scroll-container {
  @apply flex flex-col overflow-hidden min-h-0 bg-white dark:bg-dark-800 rounded-2xl border border-gray-200 dark:border-dark-700 shadow-sm;
  flex: 1 1 0px;
}

.table-scroll-container :deep(.table-wrapper) {
  @apply flex-1 overflow-x-auto overflow-y-auto;
  /* 确保横向滚动条显示在最底部 */
  scrollbar-gutter: stable;
}

.table-scroll-container :deep(table) {
  @apply w-full;
  min-width: max-content; /* 关键：确保表格宽度根据内容撑开，从而触发横向滚动 */
  display: table; /* 使用标准 table 布局以支持 sticky 列 */
}

.table-scroll-container :deep(thead) {
  @apply bg-gray-50/80 dark:bg-dark-800/80 backdrop-blur-sm;
}

.table-scroll-container :deep(tbody) {
  /* 保持默认 table-row-group 显示，不使用 block */
}

.table-scroll-container :deep(th) {
  @apply px-5 py-4 text-left text-sm font-medium text-gray-600 dark:text-dark-300 border-b border-gray-200 dark:border-dark-700;
}

.table-scroll-container :deep(td) {
  @apply px-5 py-4 text-sm text-gray-700 dark:text-gray-300 border-b border-gray-100 dark:border-dark-800;
}

/* 紧凑密度：减少单元格内边距，单屏显示更多行 */
.table-page-layout--dense .table-scroll-container :deep(th) {
  @apply px-3 py-2.5 text-xs;
}

.table-page-layout--dense .table-scroll-container :deep(td) {
  @apply px-3 py-2;
}

/* 移动端：恢复正常滚动 */
.table-page-layout.mobile-mode {
  min-height: 0;
}

.table-page-layout.mobile-mode .table-scroll-container {
  @apply h-auto overflow-visible border-none shadow-none bg-transparent;
  flex: none;
}

.table-page-layout.mobile-mode .layout-section-scrollable {
  @apply flex-none min-h-fit;
}

.table-page-layout.mobile-mode .table-scroll-container :deep(.table-wrapper) {
  @apply overflow-visible;
}

.table-page-layout.mobile-mode .table-scroll-container :deep(table) {
  @apply flex-none;
  display: table;
  min-width: 100%;
}
</style>
