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
  const model = (row.model || '').toLowerCase().trim().split('/').pop()?.replace(/-\d{4}-\d{2}-\d{2}$/, '')
  if (snapshot || !model || !/^(gpt-5\.(4|5)(-pro)?|gpt-5\.6(-sol|-terra|-luna)?|gpt-6(-astra|-sol|-luna)|gpt-6\.1-sol)$/.test(model) || total <= 272000) return null
  return { applied: true, inferred: true, mode: 'whole_request', threshold: 272000, total_input_tokens: total,
    input_multiplier: 2, output_multiplier: 1.5, cache_read_multiplier: 2, cache_write_multiplier: 2 }
}
