export const OPENAI_UPSTREAM_PROTOCOL_PLATFORM_COMPAT = 'platform_compat' as const
export const OPENAI_UPSTREAM_PROTOCOL_CODEX_NATIVE_RELAY_V1 = 'codex_native_relay_v1' as const

export type OpenAIUpstreamProtocol =
  | typeof OPENAI_UPSTREAM_PROTOCOL_PLATFORM_COMPAT
  | typeof OPENAI_UPSTREAM_PROTOCOL_CODEX_NATIVE_RELAY_V1

export function resolveOpenAIUpstreamProtocol(value: unknown): OpenAIUpstreamProtocol {
  return value === OPENAI_UPSTREAM_PROTOCOL_CODEX_NATIVE_RELAY_V1
    ? OPENAI_UPSTREAM_PROTOCOL_CODEX_NATIVE_RELAY_V1
    : OPENAI_UPSTREAM_PROTOCOL_PLATFORM_COMPAT
}
