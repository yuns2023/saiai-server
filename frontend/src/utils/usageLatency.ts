export interface UsageLatencySource {
  first_token_ms?: number | null
  duration_ms?: number | null
  output_tokens?: number | null
}

// Below these, the generation window is dominated by network jitter and the rate is noise.
export const MIN_TPS_OUTPUT_TOKENS = 20
export const MIN_TPS_WINDOW_MS = 200

export function formatLatency(ms: number | null | undefined): string {
  if (ms == null || !Number.isFinite(ms) || ms < 0) return '-'
  if (ms < 1000) return `${Math.round(ms)}ms`
  return `${(ms / 1000).toFixed(2)}s`
}

/**
 * Output speed over the generation window: output_tokens / (duration - first token).
 * Only streamed requests record a first token; output_tokens includes reasoning tokens.
 */
export function outputTokensPerSecond(log: UsageLatencySource): number | null {
  const { first_token_ms: firstToken, duration_ms: duration, output_tokens: output } = log
  if (firstToken == null || duration == null || output == null) return null
  if (output < MIN_TPS_OUTPUT_TOKENS) return null
  const windowMs = duration - firstToken
  if (!Number.isFinite(windowMs) || windowMs < MIN_TPS_WINDOW_MS) return null
  return (output * 1000) / windowMs
}

export function formatTokensPerSecond(value: number | null): string {
  return value == null ? '-' : `${value.toFixed(1)} tok/s`
}
