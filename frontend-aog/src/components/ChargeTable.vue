<script setup lang="ts">
/**
 * Statement lines for usage records. Each line unfolds into its own receipt:
 * tokens × unit price per part, the standard total, every multiplier applied,
 * and the amount actually charged. This is the product's transparency promise.
 */
import { ref } from 'vue'
import { useI18n } from 'vue-i18n'
import { intlLocale } from '@/i18n'
import { compact, cost, count, dateTime, rate, shortTime, unitPrice } from '@/lib/format'
import type { UsageLog } from '@/types'
import Icon from './Icon.vue'

withDefaults(defineProps<{ rows: UsageLog[]; loading?: boolean; showKey?: boolean }>(), { showKey: true })
const { t } = useI18n()
const open = ref<number | null>(null)

function toggle(id: number): void {
  open.value = open.value === id ? null : id
}

function perMillion(partCost: number, tokens: number): string {
  if (!tokens) return '—'
  return unitPrice((partCost / tokens) * 1_000_000)
}

function billedLabel(type: number | undefined): string {
  if (type === 0) return t('charge.billedBalance')
  if (type === 1) return t('charge.billedSubscription')
  return t('charge.billedOther')
}

function multipliers(row: UsageLog): Array<{ label: string; value: number }> {
  const list = [{ label: t('charge.groupRate'), value: row.rate_multiplier ?? 1 }]
  if (row.model_rate_multiplier != null && row.model_rate_multiplier !== 1) list.push({ label: t('charge.modelRate'), value: row.model_rate_multiplier })
  if (row.user_payg_discount_multiplier != null && row.user_payg_discount_multiplier !== 1) list.push({ label: t('charge.userDiscount'), value: row.user_payg_discount_multiplier })
  if (row.account_payg_discount_multiplier != null && row.account_payg_discount_multiplier !== 1) list.push({ label: t('charge.accountDiscount'), value: row.account_payg_discount_multiplier })
  return list
}

function parts(row: UsageLog): Array<{ label: string; tokens: number; cost: number }> {
  return [
    { label: t('charge.input'), tokens: row.input_tokens, cost: row.input_cost },
    { label: t('charge.output'), tokens: row.output_tokens, cost: row.output_cost },
    { label: t('charge.cacheWrite'), tokens: row.cache_creation_tokens, cost: row.cache_creation_cost },
    { label: t('charge.cacheRead'), tokens: row.cache_read_tokens, cost: row.cache_read_cost }
  ].filter((p) => p.tokens > 0 || p.cost > 0)
}

/**
 * Token parts only explain the price when the charge is token-priced. Per-turn
 * (native chat) and image charges are priced per call/image, so they are shown
 * as one line instead of a tokens × price formula that would not add up.
 */
function tokenPriced(row: UsageLog): boolean {
  if (row.billing_unit === 'turn') return false
  const partsSum = row.input_cost + row.output_cost + row.cache_creation_cost + row.cache_read_cost
  return partsSum > 0 || row.total_cost === 0
}

function flatLabel(row: UsageLog): string {
  if (row.billing_unit === 'turn') return t('charge.perTurn')
  if (row.image_count) return t('charge.perImage', { n: row.image_count })
  return t('charge.perCall')
}

const totalTokens = (r: UsageLog) => r.input_tokens + r.output_tokens + r.cache_creation_tokens + r.cache_read_tokens
</script>

<template>
  <div class="relative overflow-x-auto">
    <table class="ledger sm:min-w-[40rem]">
      <thead>
        <tr>
          <th class="w-[7.5rem]">{{ t('charge.time') }}</th>
          <th>{{ t('charge.model') }}</th>
          <th v-if="showKey" class="hidden md:table-cell">{{ t('charge.key') }}</th>
          <th class="amount hidden sm:table-cell">{{ t('charge.tokens') }}</th>
          <th class="amount hidden sm:table-cell">{{ t('charge.rate') }}</th>
          <th class="amount">{{ t('charge.amount') }}</th>
          <th class="w-8"><span class="sr-only">{{ t('charge.details') }}</span></th>
        </tr>
      </thead>
      <tbody v-if="loading && !rows.length">
        <tr v-for="i in 5" :key="i">
          <td :colspan="showKey ? 7 : 6"><div class="skeleton h-4 w-full" /></td>
        </tr>
      </tbody>
      <tbody v-else>
        <template v-for="row in rows" :key="row.id">
          <tr class="cursor-pointer transition-colors duration-150 hover:bg-rule-soft/60" @click="toggle(row.id)">
            <td class="num whitespace-nowrap text-ink-2" :title="dateTime(row.created_at, intlLocale())">{{ shortTime(row.created_at, intlLocale()) }}</td>
            <td class="max-w-[9rem] truncate font-mono text-xs leading-5 sm:max-w-[16rem]" :title="row.model">{{ row.model }}</td>
            <td v-if="showKey" class="hidden max-w-[10rem] truncate text-ink-2 md:table-cell">{{ row.api_key?.name || `#${row.api_key_id}` }}</td>
            <td class="amount num hidden text-ink-2 sm:table-cell" :title="row.token_usage_source === 'unknown' ? t('charge.tokensUnknown') : undefined">
              {{ row.token_usage_source === 'unknown' ? '—' : compact(totalTokens(row), intlLocale()) }}
            </td>
            <td class="amount num hidden text-ink-2 sm:table-cell">{{ rate(row.rate_multiplier) }}</td>
            <td class="amount num font-medium">{{ cost(row.actual_cost) }}</td>
            <td class="text-right">
              <button
                type="button"
                class="text-ink-3 hover:text-ink"
                :aria-expanded="open === row.id"
                :aria-label="t('charge.details')"
                @click.stop="toggle(row.id)"
              >
                <Icon name="chevronDown" :size="16" class="transition-transform duration-200" :class="open === row.id && 'rotate-180'" />
              </button>
            </td>
          </tr>
          <tr v-if="open === row.id" class="bg-shell/60">
            <td :colspan="showKey ? 7 : 6" class="!px-0 !py-0">
              <div class="breakdown grid gap-x-10 gap-y-4 px-4 py-4 sm:grid-cols-[1fr_1fr]">
                <dl class="space-y-1.5 text-sm">
                  <div v-if="!tokenPriced(row)" class="flex items-baseline justify-between gap-4">
                    <dt class="text-ink-2">{{ flatLabel(row) }}</dt>
                    <dd class="num">{{ cost(row.total_cost) }}</dd>
                  </div>
                  <div v-for="p in tokenPriced(row) ? parts(row) : []" :key="p.label" class="flex items-baseline justify-between gap-4">
                    <dt class="text-ink-2">{{ p.label }}</dt>
                    <dd class="num text-right">
                      <span class="text-ink-3">{{ count(p.tokens, intlLocale()) }} × {{ perMillion(p.cost, p.tokens) }}/M =</span>
                      {{ cost(p.cost) }}
                    </dd>
                  </div>
                  <div v-if="row.image_count && tokenPriced(row)" class="flex justify-between gap-4">
                    <dt class="text-ink-2">{{ t('charge.images') }}</dt>
                    <dd class="num">{{ row.image_count }}<span v-if="row.image_size" class="text-ink-3"> · {{ row.image_size }}</span></dd>
                  </div>
                  <div class="flex items-baseline justify-between gap-4 border-t border-rule pt-1.5">
                    <dt class="text-ink-2">{{ t('charge.standard') }}</dt>
                    <dd class="num">{{ cost(row.total_cost) }}</dd>
                  </div>
                  <p v-if="tokenPriced(row)" class="pt-1 text-xs text-ink-3">{{ t('charge.unitNote') }}</p>
                </dl>
                <dl class="space-y-1.5 text-sm">
                  <div v-for="m in multipliers(row)" :key="m.label" class="flex justify-between gap-4">
                    <dt class="text-ink-2">{{ m.label }}</dt>
                    <dd class="num">{{ rate(m.value) }}</dd>
                  </div>
                  <div class="rule-double -mx-0 pt-0" />
                  <div class="flex items-baseline justify-between gap-4 pt-0.5">
                    <dt class="font-medium">{{ billedLabel(row.billing_type) }}</dt>
                    <dd class="num text-md font-semibold">{{ cost(row.actual_cost) }}</dd>
                  </div>
                  <div v-if="row.metered_cost != null" class="flex justify-between gap-4 text-sm">
                    <dt class="text-ink-2">{{ t('charge.metered') }}</dt>
                    <dd class="num">{{ cost(row.metered_cost) }}</dd>
                  </div>
                  <div class="flex justify-between gap-4 pt-2 text-xs text-ink-3">
                    <dt>{{ t('charge.requestId') }}</dt>
                    <dd class="max-w-[14rem] truncate font-mono" :title="row.request_id">{{ row.request_id }}</dd>
                  </div>
                  <div class="flex justify-between gap-4 text-xs text-ink-3">
                    <dt>{{ t('charge.duration') }}</dt>
                    <dd class="num">
                      {{ (row.duration_ms / 1000).toFixed(2) }}s<template v-if="row.first_token_ms != null"> · {{ t('charge.firstToken') }} {{ (row.first_token_ms / 1000).toFixed(2) }}s</template>
                    </dd>
                  </div>
                </dl>
              </div>
            </td>
          </tr>
        </template>
      </tbody>
    </table>
  </div>
</template>

<style scoped>
.breakdown {
  animation: unfold 200ms cubic-bezier(0.16, 1, 0.3, 1) both;
  transform-origin: top;
}
@keyframes unfold {
  from {
    opacity: 0;
    clip-path: inset(0 0 100% 0);
  }
  to {
    opacity: 1;
    clip-path: inset(0 0 0 0);
  }
}
</style>
