<template>
  <AppLayout>
    <TablePageLayout dense>
      <template v-if="!statsCollapsed" #actions>
        <div class="grid grid-cols-2 gap-3 lg:grid-cols-4" data-testid="usage-stats-cards">
          <!-- Total Requests -->
          <div class="card flex items-start gap-3 px-4 py-2.5">
            <div class="shrink-0 rounded-lg bg-blue-100 p-1.5 dark:bg-blue-900/30">
              <Icon name="document" size="sm" class="text-blue-600 dark:text-blue-400" />
            </div>
            <div class="min-w-0 space-y-0.5">
              <p class="text-xs font-medium text-gray-500 dark:text-gray-400">
                {{ t('usage.totalRequests') }}
              </p>
              <p class="text-lg font-bold leading-tight tabular-nums text-gray-900 dark:text-white">
                {{ usageStats?.total_requests?.toLocaleString() || '0' }}
              </p>
              <p class="text-xs text-gray-500 dark:text-gray-400">
                {{ t('usage.inSelectedRange') }}
              </p>
            </div>
          </div>

          <!-- Total Tokens -->
          <div class="card flex items-start gap-3 px-4 py-2.5">
            <div class="shrink-0 rounded-lg bg-amber-100 p-1.5 dark:bg-amber-900/30">
              <Icon name="cube" size="sm" class="text-amber-600 dark:text-amber-400" />
            </div>
            <div class="min-w-0 space-y-0.5">
              <p class="text-xs font-medium text-gray-500 dark:text-gray-400">
                {{ t('tokens.total') }}
              </p>
              <TokenUsageSummary :data="usageStats" period="total" dense />
            </div>
          </div>

          <!-- Total Cost -->
          <div class="card flex items-start gap-3 px-4 py-2.5">
            <div class="shrink-0 rounded-lg bg-green-100 p-1.5 dark:bg-green-900/30">
              <Icon name="dollar" size="sm" class="text-green-600 dark:text-green-400" />
            </div>
            <UsageBillingSummary class="flex-1" :stats="usageStats" :base-cost="usageStats?.total_cost ?? null" />
          </div>

          <!-- Average Duration -->
          <div class="card flex items-start gap-3 px-4 py-2.5">
            <div class="shrink-0 rounded-lg bg-purple-100 p-1.5 dark:bg-purple-900/30">
              <Icon name="clock" size="sm" class="text-purple-600 dark:text-purple-400" />
            </div>
            <div class="min-w-0 space-y-0.5">
              <p class="text-xs font-medium text-gray-500 dark:text-gray-400">
                {{ t('usage.avgDuration') }}
              </p>
              <p class="text-lg font-bold leading-tight tabular-nums text-gray-900 dark:text-white">
                {{ formatDuration(usageStats?.average_duration_ms || 0) }}
              </p>
              <p class="text-xs text-gray-500 dark:text-gray-400">{{ t('usage.perRequest') }}</p>
            </div>
          </div>
        </div>
      </template>

      <template #filters>
        <div class="usage-filter-bar card px-4 py-2.5" data-testid="usage-filter-bar">
          <div class="flex flex-wrap items-center gap-x-4 gap-y-2">
            <!-- API Key Filter -->
            <div class="flex w-full flex-col items-start gap-1 sm:w-auto sm:flex-row sm:items-center sm:gap-2">
              <span class="whitespace-nowrap text-sm font-medium text-gray-600 dark:text-gray-300">{{ t('usage.apiKeyFilter') }}</span>
              <div class="w-44">
                <Select
                  v-model="filters.api_key_id"
                  :options="apiKeyOptions"
                  :placeholder="t('usage.allApiKeys')"
                  :aria-label="t('usage.apiKeyFilter')"
                  @change="applyFilters"
                />
              </div>
            </div>

            <!-- Date Range Filter -->
            <div class="flex w-full flex-col items-start gap-1 sm:w-auto sm:flex-row sm:items-center sm:gap-2">
              <span class="whitespace-nowrap text-sm font-medium text-gray-600 dark:text-gray-300">{{ t('usage.timeRange') }}</span>
              <DateRangePicker
                v-model:start-date="startDate"
                v-model:end-date="endDate"
                :aria-label="t('usage.timeRange')"
                @change="onDateRangeChange"
              />
            </div>

            <!-- Actions -->
            <div class="ml-auto flex flex-wrap items-center gap-2">
              <button
                type="button"
                class="btn btn-ghost btn-sm"
                :aria-expanded="!statsCollapsed"
                data-testid="usage-stats-toggle"
                @click="toggleStatsCollapsed"
              >
                <Icon :name="statsCollapsed ? 'chevronDown' : 'chevronUp'" size="xs" />
                {{ statsCollapsed ? t('usage.expandStats') : t('usage.collapseStats') }}
              </button>
              <button type="button" @click="applyFilters" :disabled="loading" class="btn btn-secondary btn-sm">
                {{ t('common.refresh') }}
              </button>
              <button type="button" @click="resetFilters" class="btn btn-secondary btn-sm">
                {{ t('common.reset') }}
              </button>
              <button type="button" @click="exportToCSV" :disabled="exporting" class="btn btn-primary btn-sm">
                <svg
                  v-if="exporting"
                  class="h-3.5 w-3.5 animate-spin"
                  fill="none"
                  viewBox="0 0 24 24"
                >
                  <circle
                    class="opacity-25"
                    cx="12"
                    cy="12"
                    r="10"
                    stroke="currentColor"
                    stroke-width="4"
                  ></circle>
                  <path
                    class="opacity-75"
                    fill="currentColor"
                    d="M4 12a8 8 0 018-8V0C5.373 0 0 5.373 0 12h4zm2 5.291A7.962 7.962 0 014 12H0c0 3.042 1.135 5.824 3 7.938l3-2.647z"
                  ></path>
                </svg>
                {{ exporting ? t('usage.exporting') : t('usage.exportCsv') }}
              </button>
            </div>
          </div>
        </div>
      </template>

      <template #table>
        <DataTable :columns="columns" :data="usageLogs" :loading="loading">
          <template #cell-api_key="{ row }">
            <span class="text-sm text-gray-900 dark:text-white">{{
              row.api_key?.name || '-'
            }}</span>
          </template>

          <template #cell-model="{ value }">
            <span class="font-medium text-gray-900 dark:text-white">{{ value }}</span>
          </template>

          <template #cell-reasoning_effort="{ row }">
            <span
              class="text-sm text-gray-900 dark:text-white"
              :class="row.reasoning_effort_inherited ? 'cursor-help border-b border-dotted border-gray-400 dark:border-gray-500' : ''"
              :title="row.reasoning_effort_inherited ? t('usage.reasoningEffortInherited') : undefined"
            >
              {{ row.billing_unit === 'turn' && row.chat_tier ? t('usage.chatTiers.' + row.chat_tier) : formatReasoningEffort(getEffectiveReasoningEffort(row)) }}
            </span>
          </template>

          <template #cell-routing="{ row }">
            <span>{{ row.group?.name ?? row.group_id ?? '—' }}</span>
            <div v-if="row.smart_switch" class="text-xs text-gray-500">
              {{ row.smart_switch.reason ? row.smart_switch.reason.split(',').map((reason: string) => t('keys.smartSwitch.reasons.' + reason)).join(' / ') : t('keys.smartSwitch.preferred') }}
            </div>
          </template>
          <template #cell-endpoint="{ row }">
            <span class="text-sm text-gray-600 dark:text-gray-300 block max-w-[320px] whitespace-normal break-all">
              {{ formatUsageEndpoints(row) }}
            </span>
          </template>

          <template #cell-session_id="{ row }">
            <span
              v-if="row.session_id"
              class="block max-w-[180px] truncate font-mono text-xs text-gray-600 dark:text-gray-400"
              :title="row.session_id"
            >
              {{ row.session_id }}
            </span>
            <span v-else class="text-sm text-gray-400 dark:text-gray-500">-</span>
          </template>

          <template #cell-stream="{ row }">
            <span
              class="inline-flex items-center rounded-sm px-2 py-0.5 text-xs font-medium"
              :class="getRequestTypeBadgeClass(row)"
            >
              {{ getRequestTypeLabel(row) }}
            </span>
          </template>

          <template #cell-tokens="{ row }">
            <div v-if="isNativeChatUsage(row)" class="text-sm" :title="t('usage.nativeChatUsageUnknown')">
              {{ t('usage.nativeChatTurn') }}
              <div v-if="row.media_type === 'image'" class="text-xs text-indigo-600 dark:text-indigo-400">
                {{ row.image_count > 0 ? t('usage.nativeChatImagesObserved', { count: row.image_count }) : t('usage.nativeChatImageCountUnknown') }}
                <div v-if="row.image_count > 0">{{ row.image_size === 'mixed' ? t('usage.imageSizeMixed') : row.image_size || t('usage.imageSizeUnknown') }}</div>
              </div>
              <div class="text-xs text-gray-500">{{ t('usage.nativeChatUsageUnknown') }}</div>
            </div>
            <!-- 图片生成请求 -->
            <div v-else-if="row.image_count > 0" class="flex items-center gap-1.5">
              <svg
                class="h-4 w-4 text-indigo-500"
                fill="none"
                stroke="currentColor"
                viewBox="0 0 24 24"
              >
                <path
                  stroke-linecap="round"
                  stroke-linejoin="round"
                  stroke-width="2"
                  d="M4 16l4.586-4.586a2 2 0 012.828 0L16 16m-2-2l1.586-1.586a2 2 0 012.828 0L20 14m-6-6h.01M6 20h12a2 2 0 002-2V6a2 2 0 00-2-2H6a2 2 0 00-2 2v12a2 2 0 002 2z"
                />
              </svg>
              <span class="font-medium text-gray-900 dark:text-white">{{ row.image_count }}{{ $t('usage.imageUnit') }}</span>
              <span class="text-gray-400">({{ row.image_size || t('usage.imageSizeUnknown') }})</span>
            </div>
            <!-- Token 请求：单行展示 输入/输出/缓存读/缓存写 -->
            <div v-else class="flex flex-wrap items-center gap-x-2 gap-y-0.5 text-sm md:flex-nowrap md:whitespace-nowrap" data-testid="usage-token-cell">
              <span class="inline-flex items-center gap-0.5">
                <Icon name="arrowDown" size="xs" class="text-emerald-500" />
                <span class="font-medium text-gray-900 dark:text-white"><TokenCount :value="row.input_tokens" :label="t('tokens.input')" /></span>
              </span>
              <span class="inline-flex items-center gap-0.5">
                <Icon name="arrowUp" size="xs" class="text-violet-500" />
                <span class="font-medium text-gray-900 dark:text-white"><TokenCount :value="row.output_tokens" :label="t('tokens.output')" /></span>
              </span>
              <span v-if="row.cache_read_tokens > 0" class="inline-flex items-center gap-0.5">
                <Icon name="inbox" size="xs" class="text-sky-500" />
                <span class="font-medium text-sky-600 dark:text-sky-400"><TokenCount :value="row.cache_read_tokens" :label="t('tokens.cacheRead')" /></span>
              </span>
              <span v-if="row.cache_creation_tokens > 0" class="inline-flex items-center gap-0.5">
                <Icon name="edit" size="xs" class="text-amber-500" />
                <span class="font-medium text-amber-600 dark:text-amber-400"><TokenCount :value="row.cache_creation_tokens" :label="t('tokens.cacheWrite')" /></span>
                <span v-if="row.cache_creation_1h_tokens > 0" class="inline-flex items-center rounded-sm px-1 py-px text-[10px] font-medium leading-tight bg-orange-100 text-orange-600 ring-1 ring-inset ring-orange-200 dark:bg-orange-500/20 dark:text-orange-400 dark:ring-orange-500/30">1h</span>
                <span v-if="row.cache_ttl_overridden" :title="t('usage.cacheTtlOverriddenHint')" class="inline-flex items-center rounded-sm px-1 py-px text-[10px] font-medium leading-tight bg-rose-100 text-rose-600 ring-1 ring-inset ring-rose-200 dark:bg-rose-500/20 dark:text-rose-400 dark:ring-rose-500/30 cursor-help">R</span>
              </span>
              <!-- Token Detail Tooltip -->
              <div
                class="group relative"
                @mouseenter="showTokenTooltip($event, row)"
                @mouseleave="hideTokenTooltip"
              >
                <div
                  class="flex h-4 w-4 cursor-help items-center justify-center rounded-full bg-gray-100 transition-colors group-hover:bg-blue-100 dark:bg-gray-700 dark:group-hover:bg-blue-900/50"
                >
                  <Icon
                    name="infoCircle"
                    size="xs"
                    class="text-gray-400 group-hover:text-blue-500 dark:text-gray-500 dark:group-hover:text-blue-400"
                  />
                </div>
              </div>
            </div>
          </template>

          <template #header-cost="{ column }">
            <UsageCostLegend :label="column.label" show-reference />
          </template>

          <template #cell-cost="{ row }">
            <UsageCostCell :row="row" @show-detail="showTooltip($event, row)" @hide-detail="hideTooltip">
              <NativeChatCharges :record="row" />
            </UsageCostCell>
          </template>

          <template #cell-first_token="{ row }">
            <span
              v-if="row.first_token_ms != null"
              class="text-sm text-gray-600 dark:text-gray-400"
            >
              {{ formatDuration(row.first_token_ms) }}
            </span>
            <span v-else class="text-sm text-gray-400 dark:text-gray-500">-</span>
          </template>

          <template #cell-duration="{ row }">
            <span class="text-sm text-gray-600 dark:text-gray-400">{{
              formatDuration(row.duration_ms)
            }}</span>
          </template>

          <template #cell-created_at="{ value }">
            <span class="text-sm text-gray-600 dark:text-gray-400">{{
              formatDateTime(value)
            }}</span>
          </template>

          <template #cell-user_agent="{ row }">
            <span v-if="row.user_agent" class="block max-w-[360px] whitespace-normal break-all text-sm text-gray-600 dark:text-gray-400 md:truncate md:break-normal" :title="row.user_agent">{{ formatUserAgent(row.user_agent) }}</span>
            <span v-else class="text-sm text-gray-400 dark:text-gray-500">-</span>
          </template>

          <template #empty>
            <EmptyState :message="t('usage.noRecords')" />
          </template>
        </DataTable>
      </template>

      <template #pagination>
        <Pagination
          v-if="pagination.total > 0"
          dense
          :page="pagination.page"
          :total="pagination.total"
          :page-size="pagination.page_size"
          @update:page="handlePageChange"
          @update:pageSize="handlePageSizeChange"
        />
      </template>
    </TablePageLayout>
  </AppLayout>

  <!-- Token Tooltip Portal -->
  <Teleport to="body">
    <div
      v-if="tokenTooltipVisible"
      class="fixed z-9999 pointer-events-none -translate-y-1/2"
      :style="{
        left: tokenTooltipPosition.x + 'px',
        top: tokenTooltipPosition.y + 'px'
      }"
    >
      <div
        class="whitespace-nowrap rounded-lg border border-gray-700 bg-gray-900 px-3 py-2.5 text-xs text-white shadow-xl dark:border-gray-600 dark:bg-gray-800"
      >
        <div class="space-y-1.5">
          <!-- Token Breakdown -->
          <div>
            <div class="text-xs font-semibold text-gray-300 mb-1">{{ t('usage.tokenDetails') }}</div>
            <div v-if="tokenTooltipData && tokenTooltipData.input_tokens > 0" class="flex items-center justify-between gap-4">
              <span class="text-gray-400">{{ t('tokens.input') }}</span>
              <span class="font-medium text-white">{{ tokenTooltipData.input_tokens.toLocaleString() }}</span>
            </div>
            <div v-if="tokenTooltipData && tokenTooltipData.output_tokens > 0" class="flex items-center justify-between gap-4">
              <span class="text-gray-400">{{ t('admin.usage.outputTokens') }}</span>
              <span class="font-medium text-white">{{ tokenTooltipData.output_tokens.toLocaleString() }}</span>
            </div>
            <div v-if="tokenTooltipData && tokenTooltipData.cache_creation_tokens > 0">
              <!-- 有 5m/1h 明细时，展开显示 -->
              <template v-if="tokenTooltipData.cache_creation_5m_tokens > 0 || tokenTooltipData.cache_creation_1h_tokens > 0">
                <div v-if="tokenTooltipData.cache_creation_5m_tokens > 0" class="flex items-center justify-between gap-4">
                  <span class="text-gray-400 flex items-center gap-1.5">
                    {{ t('admin.usage.cacheCreation5mTokens') }}
                    <span class="inline-flex items-center rounded-sm px-1 py-px text-[10px] font-medium leading-tight bg-amber-500/20 text-amber-400 ring-1 ring-inset ring-amber-500/30">5m</span>
                  </span>
                  <span class="font-medium text-white">{{ tokenTooltipData.cache_creation_5m_tokens.toLocaleString() }}</span>
                </div>
                <div v-if="tokenTooltipData.cache_creation_1h_tokens > 0" class="flex items-center justify-between gap-4">
                  <span class="text-gray-400 flex items-center gap-1.5">
                    {{ t('admin.usage.cacheCreation1hTokens') }}
                    <span class="inline-flex items-center rounded-sm px-1 py-px text-[10px] font-medium leading-tight bg-orange-500/20 text-orange-400 ring-1 ring-inset ring-orange-500/30">1h</span>
                  </span>
                  <span class="font-medium text-white">{{ tokenTooltipData.cache_creation_1h_tokens.toLocaleString() }}</span>
                </div>
              </template>
              <!-- 无明细时，只显示聚合值 -->
              <div v-else class="flex items-center justify-between gap-4">
                <span class="text-gray-400">{{ t('tokens.cacheWrite') }}</span>
                <span class="font-medium text-white">{{ tokenTooltipData.cache_creation_tokens.toLocaleString() }}</span>
              </div>
            </div>
            <div v-if="tokenTooltipData && tokenTooltipData.cache_ttl_overridden" class="flex items-center justify-between gap-4">
              <span class="text-gray-400 flex items-center gap-1.5">
                {{ t('usage.cacheTtlOverriddenLabel') }}
                <span class="inline-flex items-center rounded-sm px-1 py-px text-[10px] font-medium leading-tight bg-rose-500/20 text-rose-400 ring-1 ring-inset ring-rose-500/30">R-{{ tokenTooltipData.cache_creation_1h_tokens > 0 ? '5m' : '1H' }}</span>
              </span>
              <span class="font-medium text-rose-400">{{ tokenTooltipData.cache_creation_1h_tokens > 0 ? t('usage.cacheTtlOverridden1h') : t('usage.cacheTtlOverridden5m') }}</span>
            </div>
            <div v-if="tokenTooltipData && tokenTooltipData.cache_read_tokens > 0" class="flex items-center justify-between gap-4">
              <span class="text-gray-400">{{ t('admin.usage.cacheReadTokens') }}</span>
              <span class="font-medium text-white">{{ tokenTooltipData.cache_read_tokens.toLocaleString() }}</span>
            </div>
          </div>
          <!-- Total -->
          <div class="flex items-center justify-between gap-6 border-t border-gray-700 pt-1.5">
            <span class="text-gray-400">{{ t('tokens.total') }}</span>
            <span class="font-semibold text-blue-400">{{ ((tokenTooltipData?.input_tokens || 0) + (tokenTooltipData?.output_tokens || 0) + (tokenTooltipData?.cache_creation_tokens || 0) + (tokenTooltipData?.cache_read_tokens || 0)).toLocaleString() }}</span>
          </div>
        </div>
        <!-- Tooltip Arrow (left side) -->
        <div
          class="absolute right-full top-1/2 h-0 w-0 -translate-y-1/2 border-b-[6px] border-r-[6px] border-t-[6px] border-b-transparent border-r-gray-900 border-t-transparent dark:border-r-gray-800"
        ></div>
      </div>
    </div>
  </Teleport>

  <!-- Tooltip Portal -->
  <Teleport to="body">
    <div
      v-if="tooltipVisible"
      class="fixed z-9999 pointer-events-none -translate-y-1/2"
      :style="{
        left: tooltipPosition.x + 'px',
        top: tooltipPosition.y + 'px'
      }"
    >
      <div
        class="max-h-[calc(100vh-1rem)] overflow-y-auto whitespace-nowrap rounded-lg border border-gray-700 bg-gray-900 px-3 py-2.5 text-xs text-white shadow-xl dark:border-gray-600 dark:bg-gray-800"
      >
        <div class="space-y-1.5">
          <!-- Cost Breakdown -->
          <div class="mb-2 border-b border-gray-700 pb-1.5">
            <div class="text-xs font-semibold text-gray-300 mb-1">{{ t('usage.costDetails') }}</div>
            <div v-if="tooltipData && tooltipData.input_cost > 0" class="flex items-center justify-between gap-4">
              <span class="text-gray-400">{{ t(isNativeChatUsage(tooltipData) ? 'usage.nativeChatTurnFee' : 'admin.usage.inputCost') }}</span>
              <span class="font-medium text-white">${{ tooltipData.input_cost.toFixed(6) }}</span>
            </div>
            <div v-if="tooltipData && tooltipData.output_cost > 0" class="flex items-center justify-between gap-4">
              <span class="text-gray-400">{{ t(isNativeChatUsage(tooltipData) ? 'usage.nativeChatImageFee' : 'admin.usage.outputCost') }}</span>
              <span class="font-medium text-white">${{ tooltipData.output_cost.toFixed(6) }}</span>
            </div>
            <div v-if="tooltipData && tooltipData.input_tokens > 0" class="flex items-center justify-between gap-4">
              <span class="text-gray-400">{{ t('usage.inputTokenPrice') }}</span>
              <span class="font-medium text-sky-300">{{ formatTokenPricePerMillion(tooltipData.input_cost, tooltipData.input_tokens) }} {{ t('usage.perMillionTokens') }}</span>
            </div>
            <div v-if="tooltipData && tooltipData.output_tokens > 0" class="flex items-center justify-between gap-4">
              <span class="text-gray-400">{{ t('usage.outputTokenPrice') }}</span>
              <span class="font-medium text-violet-300">{{ formatTokenPricePerMillion(tooltipData.output_cost, tooltipData.output_tokens) }} {{ t('usage.perMillionTokens') }}</span>
            </div>
            <div v-if="tooltipData && tooltipData.cache_creation_5m_cost > 0" class="flex items-center justify-between gap-4">
              <span class="text-gray-400">{{ t('admin.usage.cacheCreation5mCost') }}</span>
              <span class="font-medium text-white">${{ tooltipData.cache_creation_5m_cost.toFixed(6) }}</span>
            </div>
            <div v-if="tooltipData && tooltipData.cache_creation_5m_cost > 0 && tooltipData.cache_creation_5m_tokens > 0" class="flex items-center justify-between gap-4">
              <span class="text-gray-400">{{ t('admin.usage.cacheCreation5mPrice') }}</span>
              <span class="font-medium text-amber-300">{{ formatTokenPricePerMillion(tooltipData.cache_creation_5m_cost, tooltipData.cache_creation_5m_tokens) }} {{ t('usage.perMillionTokens') }}</span>
            </div>
            <div v-if="tooltipData && tooltipData.cache_creation_1h_cost > 0" class="flex items-center justify-between gap-4">
              <span class="text-gray-400">{{ t('admin.usage.cacheCreation1hCost') }}</span>
              <span class="font-medium text-white">${{ tooltipData.cache_creation_1h_cost.toFixed(6) }}</span>
            </div>
            <div v-if="tooltipData && tooltipData.cache_creation_1h_cost > 0 && tooltipData.cache_creation_1h_tokens > 0" class="flex items-center justify-between gap-4">
              <span class="text-gray-400">{{ t('admin.usage.cacheCreation1hPrice') }}</span>
              <span class="font-medium text-orange-300">{{ formatTokenPricePerMillion(tooltipData.cache_creation_1h_cost, tooltipData.cache_creation_1h_tokens) }} {{ t('usage.perMillionTokens') }}</span>
            </div>
            <div v-if="tooltipData && tooltipData.cache_creation_cost > 0 && tooltipData.cache_creation_5m_cost <= 0 && tooltipData.cache_creation_1h_cost <= 0" class="flex items-center justify-between gap-4">
              <span class="text-gray-400">{{ t('admin.usage.cacheCreationCost') }}</span>
              <span class="font-medium text-white">${{ tooltipData.cache_creation_cost.toFixed(6) }}</span>
            </div>
            <div v-if="tooltipData && tooltipData.cache_creation_cost > 0 && tooltipData.cache_creation_tokens > 0 && tooltipData.cache_creation_5m_cost <= 0 && tooltipData.cache_creation_1h_cost <= 0" class="flex items-center justify-between gap-4">
              <span class="text-gray-400">{{ t('admin.usage.cacheCreationPrice') }}</span>
              <span class="font-medium text-amber-300">{{ formatTokenPricePerMillion(tooltipData.cache_creation_cost, tooltipData.cache_creation_tokens) }} {{ t('usage.perMillionTokens') }}</span>
            </div>
            <div v-if="tooltipData && tooltipData.cache_read_cost > 0" class="flex items-center justify-between gap-4">
              <span class="text-gray-400">{{ t('admin.usage.cacheReadCost') }}</span>
              <span class="font-medium text-white">${{ tooltipData.cache_read_cost.toFixed(6) }}</span>
            </div>
            <div v-if="tooltipData && tooltipData.cache_read_cost > 0 && tooltipData.cache_read_tokens > 0" class="flex items-center justify-between gap-4">
              <span class="text-gray-400">{{ t('admin.usage.cacheReadPrice') }}</span>
              <span class="font-medium text-sky-300">{{ formatTokenPricePerMillion(tooltipData.cache_read_cost, tooltipData.cache_read_tokens) }} {{ t('usage.perMillionTokens') }}</span>
            </div>
          </div>
          <!-- Rate and Summary -->
          <div class="flex items-center justify-between gap-6">
            <span class="text-gray-400">{{ t('usage.serviceTier') }}</span>
            <span class="font-semibold text-cyan-300">{{ getUsageServiceTierLabel(tooltipData?.service_tier, t) }}</span>
          </div>
          <div class="flex items-center justify-between gap-6">
            <span class="text-gray-400">{{ t('usage.rate') }}</span>
            <span class="font-semibold text-blue-400"
              >{{ (tooltipData?.rate_multiplier || 1).toFixed(2) }}x</span
            >
          </div>
          <div class="flex items-center justify-between gap-6">
            <span class="text-gray-400">{{ t('usage.groupModelMultiplier') }}</span>
            <span class="font-semibold text-blue-400">{{ (tooltipData?.model_rate_multiplier ?? 1).toFixed(4) }}x</span>
          </div>
          <div class="flex items-center justify-between gap-6">
            <span class="text-gray-400">{{ t('usage.paygUserDiscount') }}</span>
            <span class="font-semibold text-blue-400">{{ (tooltipData?.user_payg_discount_multiplier ?? 1).toFixed(4) }}x</span>
          </div>
          <div v-if="tooltipData?.account_payg_discount_multiplier != null && tooltipData.account_payg_discount_multiplier !== 1" class="flex items-center justify-between gap-6">
            <span class="text-gray-400">{{ t('usage.legacyPaygAccountDiscount') }}</span>
            <span class="font-semibold text-amber-400">{{ tooltipData.account_payg_discount_multiplier.toFixed(4) }}x</span>
          </div>
          <div class="flex items-center justify-between gap-6">
            <span class="text-gray-400">{{ t('usage.original') }}</span>
            <span class="font-medium text-white">${{ tooltipData?.total_cost.toFixed(6) }}</span>
          </div>
          <div class="flex items-center justify-between gap-6 border-t border-gray-700 pt-1.5">
            <span class="text-gray-400">{{ t(usageBillingLabel(tooltipData?.billing_type)) }}</span>
            <span class="font-semibold text-green-400"
              >${{ tooltipData?.actual_cost.toFixed(6) }}</span
            >
          </div>
        </div>
        <!-- Tooltip Arrow (left side) -->
        <div
          class="absolute right-full top-1/2 h-0 w-0 -translate-y-1/2 border-b-[6px] border-r-[6px] border-t-[6px] border-b-transparent border-r-gray-900 border-t-transparent dark:border-r-gray-800"
        ></div>
      </div>
    </div>
  </Teleport>
</template>

<script setup lang="ts">
import UsageBillingSummary from '@/components/common/UsageBillingSummary.vue'
import { usageBillingLabel } from '@/utils/usageBilling'
import TokenUsageSummary from '@/components/common/TokenUsageSummary.vue'
import TokenCount from '@/components/common/TokenCount.vue'
import { ref, computed, reactive, onMounted } from 'vue'
import { useI18n } from 'vue-i18n'
import { useRoute } from 'vue-router'
import { useAppStore } from '@/stores/app'
import { usageAPI, keysAPI } from '@/api'
import AppLayout from '@/components/layout/AppLayout.vue'
import TablePageLayout from '@/components/layout/TablePageLayout.vue'
import DataTable from '@/components/common/DataTable.vue'
import Pagination from '@/components/common/Pagination.vue'
import EmptyState from '@/components/common/EmptyState.vue'
import Select from '@/components/common/Select.vue'
import DateRangePicker from '@/components/common/DateRangePicker.vue'
import Icon from '@/components/icons/Icon.vue'
import type { UsageLog, ApiKey, UsageQueryParams, UsageStatsResponse } from '@/types'
import type { Column } from '@/components/common/types'
import { formatDateTime, formatReasoningEffort, getEffectiveReasoningEffort } from '@/utils/format'
import { escapeCsvCell } from '@/utils/csv'
import { getPersistedPageSize } from '@/composables/usePersistedPageSize'
import { formatTokenPricePerMillion } from '@/utils/usagePricing'
import { isNativeChatUsage } from '@/utils/nativeChatUsage'
import NativeChatCharges from '@/components/common/NativeChatCharges.vue'
import UsageCostCell from '@/components/common/UsageCostCell.vue'
import UsageCostLegend from '@/components/common/UsageCostLegend.vue'
import { getUsageServiceTierLabel } from '@/utils/usageServiceTier'
import { resolveUsageRequestType } from '@/utils/usageRequestType'

const { t } = useI18n()
const appStore = useAppStore()
const route = useRoute()
const routeQuery = route?.query ?? {}

let abortController: AbortController | null = null

// Tooltip state
const tooltipVisible = ref(false)
const tooltipPosition = ref({ x: 0, y: 0 })
const tooltipData = ref<UsageLog | null>(null)


// Token tooltip state
const tokenTooltipVisible = ref(false)
const tokenTooltipPosition = ref({ x: 0, y: 0 })
const tokenTooltipData = ref<UsageLog | null>(null)

// Usage stats from API
const usageStats = ref<UsageStatsResponse | null>(null)

// Collapsible stat cards (persisted) so short viewports can give the table more rows
const STATS_COLLAPSED_STORAGE_KEY = 'user-usage-stats-collapsed'
const readStatsCollapsed = (): boolean => {
  try {
    return localStorage.getItem(STATS_COLLAPSED_STORAGE_KEY) === 'true'
  } catch {
    return false
  }
}
const statsCollapsed = ref(readStatsCollapsed())
const toggleStatsCollapsed = () => {
  statsCollapsed.value = !statsCollapsed.value
  try {
    localStorage.setItem(STATS_COLLAPSED_STORAGE_KEY, String(statsCollapsed.value))
  } catch {
    // ignore storage failures (private mode / quota)
  }
}

const columns = computed<Column[]>(() => [
  { key: 'api_key', label: t('usage.apiKeyFilter'), sortable: false },
  { key: 'routing', label: t('keys.smartSwitch.actualGroup'), sortable: false },
  { key: 'model', label: t('usage.model'), sortable: true },
  { key: 'reasoning_effort', label: t('usage.reasoningEffort'), sortable: false },
  { key: 'endpoint', label: t('usage.endpoint'), sortable: false },
  { key: 'session_id', label: t('usage.sessionId'), sortable: false },
  { key: 'stream', label: t('usage.type'), sortable: false },
  { key: 'tokens', label: t('usage.tokens'), sortable: false },
  { key: 'cost', label: t('usage.cost'), sortable: false },
  { key: 'first_token', label: t('usage.firstToken'), sortable: false },
  { key: 'duration', label: t('usage.duration'), sortable: false },
  { key: 'created_at', label: t('usage.time'), sortable: true },
  { key: 'user_agent', label: t('usage.userAgent'), sortable: false }
])

const usageLogs = ref<UsageLog[]>([])
const apiKeys = ref<ApiKey[]>([])
const loading = ref(false)
const exporting = ref(false)

const apiKeyOptions = computed(() => {
  return [
    { value: null, label: t('usage.allApiKeys') },
    ...apiKeys.value.map((key) => ({
      value: key.id,
      label: key.name
    }))
  ]
})

// Helper function to format date in local timezone
const formatLocalDate = (date: Date): string => {
  return `${date.getFullYear()}-${String(date.getMonth() + 1).padStart(2, '0')}-${String(date.getDate()).padStart(2, '0')}`
}

// Initialize date range immediately
const now = new Date()
const weekAgo = new Date(now)
weekAgo.setDate(weekAgo.getDate() - 6)

const firstQueryValue = (value: unknown): string => {
  if (Array.isArray(value)) return typeof value[0] === 'string' ? value[0] : ''
  return typeof value === 'string' ? value : ''
}
const validDateQuery = (value: unknown): string | undefined => {
  const candidate = firstQueryValue(value)
  return /^\d{4}-\d{2}-\d{2}$/.test(candidate) ? candidate : undefined
}
const parsedAPIKeyID = Number(firstQueryValue(routeQuery.api_key_id))
const initialAPIKeyID =
  Number.isSafeInteger(parsedAPIKeyID) && parsedAPIKeyID > 0 ? parsedAPIKeyID : undefined
const browserTimezone = Intl.DateTimeFormat().resolvedOptions().timeZone || undefined

// Date range state
const startDate = ref(validDateQuery(routeQuery.start_date) || formatLocalDate(weekAgo))
const endDate = ref(validDateQuery(routeQuery.end_date) || formatLocalDate(now))

const filters = ref<UsageQueryParams>({
  api_key_id: initialAPIKeyID,
  start_date: undefined,
  end_date: undefined
})

// Initialize filters with date range
filters.value.start_date = startDate.value
filters.value.end_date = endDate.value

// Handle date range change from DateRangePicker
const onDateRangeChange = (range: {
  startDate: string
  endDate: string
  preset: string | null
}) => {
  filters.value.start_date = range.startDate
  filters.value.end_date = range.endDate
  applyFilters()
}

const pagination = reactive({
  page: 1,
  page_size: getPersistedPageSize(),
  total: 0,
  pages: 0
})

const formatDuration = (ms: number): string => {
  if (ms < 1000) return `${ms.toFixed(0)}ms`
  return `${(ms / 1000).toFixed(2)}s`
}

const formatUserAgent = (ua: string): string => {
  return ua
}

const getRequestTypeLabel = (log: UsageLog): string => {
  const requestType = resolveUsageRequestType(log)
  if (requestType === 'ws_v2') return t('usage.ws')
  if (requestType === 'stream') return t('usage.stream')
  if (requestType === 'sync') return t('usage.sync')
  return t('usage.unknown')
}

const getRequestTypeBadgeClass = (log: UsageLog): string => {
  const requestType = resolveUsageRequestType(log)
  if (requestType === 'ws_v2') return 'bg-violet-100 text-violet-800 dark:bg-violet-900 dark:text-violet-200'
  if (requestType === 'stream') return 'bg-blue-100 text-blue-800 dark:bg-blue-900 dark:text-blue-200'
  if (requestType === 'sync') return 'bg-gray-100 text-gray-800 dark:bg-gray-700 dark:text-gray-200'
  return 'bg-amber-100 text-amber-800 dark:bg-amber-900 dark:text-amber-200'
}

const getRequestTypeExportText = (log: UsageLog): string => {
  const requestType = resolveUsageRequestType(log)
  if (requestType === 'ws_v2') return 'WS'
  if (requestType === 'stream') return 'Stream'
  if (requestType === 'sync') return 'Sync'
  return 'Unknown'
}

const formatUsageEndpoints = (log: UsageLog): string => {
  const inbound = log.inbound_endpoint?.trim()
  return inbound || '-'
}


const loadUsageLogs = async () => {
  if (abortController) {
    abortController.abort()
  }
  const currentAbortController = new AbortController()
  abortController = currentAbortController
  const { signal } = currentAbortController
  loading.value = true
  try {
    const params: UsageQueryParams = {
      page: pagination.page,
      page_size: pagination.page_size,
      ...filters.value,
      timezone: browserTimezone
    }

    const response = await usageAPI.query(params, { signal })
    if (signal.aborted) {
      return
    }
    usageLogs.value = response.items
    pagination.total = response.total
    pagination.pages = response.pages
  } catch (error) {
    if (signal.aborted) {
      return
    }
    const abortError = error as { name?: string; code?: string }
    if (abortError?.name === 'AbortError' || abortError?.code === 'ERR_CANCELED') {
      return
    }
    appStore.showError(t('usage.failedToLoad'))
  } finally {
    if (abortController === currentAbortController) {
      loading.value = false
    }
  }
}

const loadApiKeys = async () => {
  try {
    const response = await keysAPI.list(1, 100)
    apiKeys.value = response.items
    if (initialAPIKeyID && !apiKeys.value.some((key) => key.id === initialAPIKeyID)) {
      const selectedKey = await keysAPI.getById(initialAPIKeyID)
      apiKeys.value.push(selectedKey)
    }
  } catch (error) {
    console.error('Failed to load API keys:', error)
  }
}

const loadUsageStats = async () => {
  try {
    const apiKeyId = filters.value.api_key_id ? Number(filters.value.api_key_id) : undefined
    const stats = await usageAPI.getStatsByDateRange(
      filters.value.start_date || startDate.value,
      filters.value.end_date || endDate.value,
      apiKeyId,
      browserTimezone
    )
    usageStats.value = stats
  } catch (error) {
    console.error('Failed to load usage stats:', error)
  }
}

const applyFilters = () => {
  pagination.page = 1
  loadUsageLogs()
  loadUsageStats()
}

const resetFilters = () => {
  filters.value = {
    api_key_id: undefined,
    start_date: undefined,
    end_date: undefined
  }
  // Reset date range to default (last 7 days)
  const now = new Date()
  const weekAgo = new Date(now)
  weekAgo.setDate(weekAgo.getDate() - 6)
  startDate.value = formatLocalDate(weekAgo)
  endDate.value = formatLocalDate(now)
  filters.value.start_date = startDate.value
  filters.value.end_date = endDate.value
  pagination.page = 1
  loadUsageLogs()
  loadUsageStats()
}

const handlePageChange = (page: number) => {
  pagination.page = page
  loadUsageLogs()
}

const handlePageSizeChange = (pageSize: number) => {
  pagination.page_size = pageSize
  pagination.page = 1
  loadUsageLogs()
}

const exportToCSV = async () => {
  if (pagination.total === 0) {
    appStore.showWarning(t('usage.noDataToExport'))
    return
  }

  exporting.value = true
  appStore.showInfo(t('usage.preparingExport'))

  try {
    const allLogs: UsageLog[] = []
    const pageSize = 100 // Use a larger page size for export to reduce requests
    const totalRequests = Math.ceil(pagination.total / pageSize)

    for (let page = 1; page <= totalRequests; page++) {
      const params: UsageQueryParams = {
        page: page,
        page_size: pageSize,
        ...filters.value,
        timezone: browserTimezone
      }
      const response = await usageAPI.query(params)
      allLogs.push(...response.items)
    }

    if (allLogs.length === 0) {
      appStore.showWarning(t('usage.noDataToExport'))
      return
    }

    const headers = [
      'Time',
      'API Key Name',
      'Model',
      'Reasoning Effort',
      'Inbound Endpoint',
      'Session ID',
      'Type',
      'Input Tokens',
      'Output Tokens',
      'Cache Read Tokens',
      'Cache Creation Tokens',
      'Rate Multiplier',
      t('usage.groupModelMultiplier'),
      t('usage.paygUserDiscount'),
      t('usage.legacyPaygAccountDiscount'),
      t('usage.billingSource'),
      t('usage.userBilled'),
      'Original Cost',
      t('hostedUsage.reference'),
      'First Token (ms)',
      'Duration (ms)'
    ]
    const rows = allLogs.map((log) =>
      [
        log.created_at,
        log.api_key?.name || '',
        log.model,
        formatReasoningEffort(getEffectiveReasoningEffort(log)),
        log.inbound_endpoint || '',
        log.session_id || '',
        getRequestTypeExportText(log),
        log.input_tokens,
        log.output_tokens,
        log.cache_read_tokens,
        log.cache_creation_tokens,
        log.rate_multiplier,
        log.model_rate_multiplier ?? 1,
        log.user_payg_discount_multiplier ?? 1,
        log.account_payg_discount_multiplier ?? 1,
        t(usageBillingLabel(log.billing_type)),
        log.actual_cost.toFixed(8),
        log.total_cost.toFixed(8),
        log.metered_cost?.toFixed(8) ?? '',
        log.first_token_ms ?? '',
        log.duration_ms
      ].map(escapeCsvCell)
    )

    const csvContent = [
      headers.map(escapeCsvCell).join(','),
      ...rows.map((row) => row.join(','))
    ].join('\n')

    const blob = new Blob([csvContent], { type: 'text/csv;charset=utf-8;' })
    const url = window.URL.createObjectURL(blob)
    const link = document.createElement('a')
    link.href = url
    link.download = `usage_${filters.value.start_date}_to_${filters.value.end_date}.csv`
    link.click()
    window.URL.revokeObjectURL(url)

    appStore.showSuccess(t('usage.exportSuccess'))
  } catch (error) {
    appStore.showError(t('usage.exportFailed'))
    console.error('CSV Export failed:', error)
  } finally {
    exporting.value = false
  }
}

// Tooltip functions
const showTooltip = (event: Event, row: UsageLog) => {
  const target = event.currentTarget as HTMLElement
  const rect = target.getBoundingClientRect()

  tooltipData.value = row
  // Position to the right of the icon, vertically centered
  tooltipPosition.value.x = rect.right + 8
  tooltipPosition.value.y = rect.top + rect.height / 2
  tooltipVisible.value = true
}

const hideTooltip = () => {
  tooltipVisible.value = false
  tooltipData.value = null
}

// Token tooltip functions
const showTokenTooltip = (event: MouseEvent, row: UsageLog) => {
  const target = event.currentTarget as HTMLElement
  const rect = target.getBoundingClientRect()

  tokenTooltipData.value = row
  tokenTooltipPosition.value.x = rect.right + 8
  tokenTooltipPosition.value.y = rect.top + rect.height / 2
  tokenTooltipVisible.value = true
}

const hideTokenTooltip = () => {
  tokenTooltipVisible.value = false
  tokenTooltipData.value = null
}

onMounted(() => {
  loadApiKeys()
  loadUsageLogs()
  loadUsageStats()
})
</script>

<style scoped>
@reference "../../style.css";

/* Compact filter controls: single row on desktop */
.usage-filter-bar :deep(.select-trigger),
.usage-filter-bar :deep(.date-picker-trigger) {
  @apply py-1.5;
}
</style>
