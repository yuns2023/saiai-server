import { describe, expect, it } from 'vitest'

import {
  OPENAI_UPSTREAM_PROTOCOL_CODEX_NATIVE_RELAY_V1,
  OPENAI_UPSTREAM_PROTOCOL_PLATFORM_COMPAT,
  resolveOpenAIUpstreamProtocol
} from '@/utils/openaiUpstreamProtocol'

describe('OpenAI upstream protocol', () => {
  it('recognizes the versioned native relay protocol', () => {
    expect(resolveOpenAIUpstreamProtocol(OPENAI_UPSTREAM_PROTOCOL_CODEX_NATIVE_RELAY_V1)).toBe(
      OPENAI_UPSTREAM_PROTOCOL_CODEX_NATIVE_RELAY_V1
    )
  })

  it('fails closed to platform compatibility', () => {
    expect(resolveOpenAIUpstreamProtocol(undefined)).toBe(OPENAI_UPSTREAM_PROTOCOL_PLATFORM_COMPAT)
    expect(resolveOpenAIUpstreamProtocol('future_mode')).toBe(OPENAI_UPSTREAM_PROTOCOL_PLATFORM_COMPAT)
  })
})
