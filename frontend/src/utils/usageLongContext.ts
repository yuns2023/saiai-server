import type { AdminUsageLog, LongContextPricing } from '@/types'
import { isNativeChatUsage } from './nativeChatUsage'

type Record = Pick<AdminUsageLog, 'model' | 'input_tokens' | 'cache_read_tokens' | 'cache_creation_tokens'> & Partial<AdminUsageLog>

export function usageLongContext(row?: Record | null): LongContextPricing | null {
  if (!row || isNativeChatUsage(row) || (row.image_count || 0) > 0 || /\/images\/(generations|edits)$/.test(row.inbound_endpoint || '')) return null
  const snapshot = row.pricing_snapshot
  // Explicit calculator evidence takes precedence over all inference, including
  // the recorded tier and future changes to model policies.
  if (snapshot?.long_context) return snapshot.long_context.applied ? snapshot.long_context : null
  const total = (row.input_tokens || 0) + (row.cache_read_tokens || 0) + (row.cache_creation_tokens || 0)
  if (snapshot?.long_context_extra_multiplier && snapshot.long_context_threshold &&
      row.input_tokens + row.cache_read_tokens > snapshot.long_context_threshold) {
    return { applied: true, mode: 'excess_input', threshold: snapshot.long_context_threshold,
      total_input_tokens: row.input_tokens + row.cache_read_tokens, input_multiplier: snapshot.long_context_extra_multiplier,
      output_multiplier: 1, cache_read_multiplier: snapshot.long_context_extra_multiplier, cache_write_multiplier: 1 }
  }
  if ((snapshot?.service_tier || row.service_tier || '').trim().toLowerCase() === 'priority') return null
  const policy = snapshot?.effective
  if (policy?.long_context_threshold) {
    const info: LongContextPricing = { applied: true, mode: 'whole_request', threshold: policy.long_context_threshold,
      total_input_tokens: total, input_multiplier: policy.long_context_input_multiplier || 1,
      output_multiplier: policy.long_context_output_multiplier || 1, cache_read_multiplier: policy.long_context_cache_read_multiplier || 1,
      cache_write_multiplier: policy.long_context_cache_write_multiplier || 1 }
    return total > info.threshold && Math.max(info.input_multiplier, info.output_multiplier, info.cache_read_multiplier, info.cache_write_multiplier) > 1 ? info : null
  }
  // Pre-snapshot rows have no provable historical price policy. Restrict the
  // advisory to known models and label it as inference; never reprice them.
  if (snapshot || !isKnownLongContextModel(row.model) || total <= 272000) return null
  return { applied: true, inferred: true, mode: 'whole_request', threshold: 272000, total_input_tokens: total,
    input_multiplier: 2, output_multiplier: 1.5, cache_read_multiplier: 2, cache_write_multiplier: 2 }
}

function isKnownLongContextModel(model?: string | null): boolean {
  const name = (model || '').toLowerCase().trim().split('/').pop()?.replace(/-\d{4}-\d{2}-\d{2}$/, '')
  return !!name && /^(gpt-5\.(4|5)(-pro)?|gpt-5\.6(-sol|-terra|-luna)?|gpt-6(-astra|-sol|-luna)|gpt-6\.1-sol)$/.test(name)
}

// The threshold of the model's long-context price tier, in the same order of
// evidence as usageLongContext. A tier needs a positive threshold and at least
// one multiplier above 1; an explicit zero threshold means no tier.
function longContextTierThreshold(row: Record): number {
  const snapshot = row.pricing_snapshot
  const tier = (threshold?: number, ...multipliers: Array<number | undefined>) =>
    (threshold || 0) > 0 && Math.max(...multipliers.map(m => m || 1)) > 1 ? threshold as number : 0
  if (snapshot?.long_context) {
    const lc = snapshot.long_context
    return tier(lc.threshold, lc.input_multiplier, lc.output_multiplier, lc.cache_read_multiplier, lc.cache_write_multiplier)
  }
  if (snapshot?.long_context_extra_multiplier && snapshot.long_context_threshold) return tier(snapshot.long_context_threshold, snapshot.long_context_extra_multiplier)
  if (snapshot) {
    const policy = snapshot.effective
    return tier(policy?.long_context_threshold, policy?.long_context_input_multiplier, policy?.long_context_output_multiplier,
      policy?.long_context_cache_read_multiplier, policy?.long_context_cache_write_multiplier)
  }
  return isKnownLongContextModel(row.model) ? 272000 : 0
}

// A size observation against the model's own long-context price tier, shown
// even when no surcharge applied (for example Priority). Models without such a
// tier (Claude, older GPT) get no marker. Do not use this to reprice usage.
export function usageInputContext(row?: Record | null): { total: number, threshold: number } | null {
  if (!row || isNativeChatUsage(row) || (row.image_count || 0) > 0 || /\/images\/(generations|edits)$/.test(row.inbound_endpoint || '')) return null
  const threshold = longContextTierThreshold(row)
  const total = (row.input_tokens || 0) + (row.cache_read_tokens || 0) + (row.cache_creation_tokens || 0)
  return threshold > 0 && total > threshold ? { total, threshold } : null
}
