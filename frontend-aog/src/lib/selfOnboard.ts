/**
 * Self-onboarding of a hosted Claude account: form checks done before the
 * request (the server checks everything again) and the mapping from the
 * server's stable error codes to the message keys under `onboard.errors`.
 */
import type { ProxyProtocol, SelfOnboardProxy } from '@/api/hosted'

export const PROXY_PROTOCOLS: readonly ProxyProtocol[] = ['socks5h', 'socks5', 'http', 'https']
export const MAX_DISPLAY_NAME = 64
/** Proxy username / password: at most this many characters and MAX_PROXY_FIELD_BYTES bytes each. */
export const MAX_PROXY_CRED_CHARS = 100
export const MAX_PROXY_FIELD_BYTES = 255
/** Longest DNS name. */
export const MAX_PROXY_HOST_BYTES = 253

export interface ProxyForm {
  protocol: ProxyProtocol
  host: string
  /** As typed; validated as an integer 1..65535. */
  port: string
  username: string
  password: string
}

export type ProxyField = 'host' | 'port' | 'auth' | 'username' | 'password' | 'protocol'
/** Message key under `onboard.check` per field; empty when the form is fine. */
export type ProxyProblems = Partial<Record<ProxyField, string>>

export function emptyProxy(): ProxyForm {
  return { protocol: 'socks5h', host: '', port: '', username: '', password: '' }
}

// eslint-disable-next-line no-control-regex
const CONTROL = /[\u0000-\u001f\u007f-\u009f]/

export function hasControlChars(s: string): boolean {
  return CONTROL.test(s)
}

function utf8Bytes(s: string): number {
  return new TextEncoder().encode(s).length
}

/** True when nothing at all was typed into the proxy fields. */
export function proxyBlank(p: ProxyForm): boolean {
  return !p.host.trim() && !p.port.trim() && !p.username && !p.password
}

/** Host without surrounding IPv6 brackets. */
function bareHost(host: string): string {
  const h = host.trim()
  return h.startsWith('[') && h.endsWith(']') ? h.slice(1, -1) : h
}

/**
 * Checks the proxy fields. A blank form is fine unless `required`. Host takes
 * a domain or an IP (no scheme, path or credentials); whether it is a public
 * address is decided by the server.
 */
export function checkProxy(p: ProxyForm, required: boolean): ProxyProblems {
  const out: ProxyProblems = {}
  if (proxyBlank(p)) {
    if (required) out.host = 'proxyRequired'
    return out
  }
  if (!PROXY_PROTOCOLS.includes(p.protocol)) out.protocol = 'protocolInvalid'

  const host = bareHost(p.host)
  if (!host) out.host = 'hostRequired'
  else if (/[\s/@?#\\]/.test(host) || hasControlChars(host) || host.includes('://') || utf8Bytes(host) > MAX_PROXY_HOST_BYTES) out.host = 'hostInvalid'

  const port = p.port.trim()
  if (!port) out.port = 'portRequired'
  else if (!/^\d{1,5}$/.test(port) || Number(port) < 1 || Number(port) > 65535) out.port = 'portInvalid'

  if (!!p.username !== !!p.password) out.auth = 'authPair'
  if (p.username && !credOk(p.username)) out.username = 'credInvalid'
  if (p.password && !credOk(p.password)) out.password = 'credInvalid'
  return out
}

function credOk(v: string): boolean {
  return !hasControlChars(v) && [...v].length <= MAX_PROXY_CRED_CHARS && utf8Bytes(v) <= MAX_PROXY_FIELD_BYTES
}

/** The request body part for a valid, non-blank proxy form; undefined when blank. */
export function proxyPayload(p: ProxyForm): SelfOnboardProxy | undefined {
  if (proxyBlank(p)) return undefined
  const out: SelfOnboardProxy = { protocol: p.protocol, host: bareHost(p.host), port: Number(p.port.trim()) }
  if (p.username && p.password) {
    out.username = p.username
    out.password = p.password
  }
  return out
}

/** Message key under `onboard.check`, or '' when the display name is fine. */
export function checkDisplayName(name: string): string {
  const n = name.trim()
  if (!n) return 'nameRequired'
  if ([...n].length > MAX_DISPLAY_NAME) return 'nameTooLong'
  if (hasControlChars(n)) return 'nameInvalid'
  return ''
}

/**
 * Normalises what the user pasted into `code#state`. Accepts the code shown on
 * the callback page as is, or the callback URL itself (`?code=…&state=…`).
 * Returns '' when it does not look like an authorisation code.
 */
export function normalizeAuthCode(raw: string): string {
  const s = raw.trim()
  if (!s) return ''
  if (/^https?:\/\//i.test(s)) {
    try {
      const u = new URL(s)
      const code = u.searchParams.get('code')
      const state = u.searchParams.get('state') || u.hash.replace(/^#/, '')
      if (code && state) return normalizeAuthCode(`${code}#${state}`)
    } catch {
      // Not a URL after all.
    }
    return ''
  }
  return /^[^\s#]+#[^\s#]+$/.test(s) ? s : ''
}

export const ONBOARD_ERROR_CODES = [
  'SELF_ONBOARD_DISABLED',
  'SELF_ONBOARD_PROXY_REQUIRED',
  'SELF_ONBOARD_PROXY_INVALID',
  'SELF_ONBOARD_PROXY_UNAVAILABLE',
  'SELF_ONBOARD_SESSION_INVALID',
  'SELF_ONBOARD_EXCHANGE_FAILED',
  'SELF_ONBOARD_IDENTITY_MISSING',
  'SELF_ONBOARD_ALREADY_HOSTED',
  'SELF_ONBOARD_PENDING_LIMIT',
  'SELF_ONBOARD_RATE_LIMITED',
  'SELF_ONBOARD_NOT_FOUND',
  'SELF_ONBOARD_INVALID_STATE',
  'SELF_ONBOARD_INVALID_INPUT',
  'SELF_ONBOARD_UNAVAILABLE'
] as const
export type OnboardErrorCode = (typeof ONBOARD_ERROR_CODES)[number]

/**
 * Errors the server raises before it uses up the authorisation link (input
 * checks, limits): the same link can be used again after fixing the input.
 * Every other failure needs a new link.
 */
const LINK_STILL_VALID: ReadonlySet<string> = new Set([
  'SELF_ONBOARD_PROXY_REQUIRED',
  'SELF_ONBOARD_PROXY_INVALID',
  'SELF_ONBOARD_PENDING_LIMIT',
  'SELF_ONBOARD_RATE_LIMITED',
  'SELF_ONBOARD_INVALID_INPUT'
])

/** The stable error code of a rejected API call (`reason` in the envelope), if any. */
export function onboardErrorCode(err: unknown): OnboardErrorCode | null {
  const e = (err ?? {}) as { reason?: unknown; code?: unknown }
  for (const v of [e.reason, e.code]) {
    if (typeof v === 'string' && (ONBOARD_ERROR_CODES as readonly string[]).includes(v)) return v as OnboardErrorCode
  }
  return null
}

/** Message key under `onboard.errors` for a failed call. */
export function onboardErrorKey(err: unknown): string {
  const code = onboardErrorCode(err)
  if (code) return code
  const status = (err as { status?: unknown } | undefined)?.status
  if (status === 0) return 'network'
  if (status === 429) return 'SELF_ONBOARD_RATE_LIMITED'
  return 'generic'
}

/** The id the server logged a failed exchange under (metadata.correlation_id), for the user to quote to an admin. */
export function onboardCorrelationId(err: unknown): string {
  const id = (err as { metadata?: Record<string, unknown> } | null)?.metadata?.correlation_id
  return typeof id === 'string' && /^[\w.:-]{1,128}$/.test(id) ? id : ''
}

/** Whether a failed submission used up the authorisation link (the user must generate a new one). */
export function submitNeedsNewLink(err: unknown): boolean {
  const code = onboardErrorCode(err)
  return !code || !LINK_STILL_VALID.has(code)
}
