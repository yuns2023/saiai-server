import type { OpsAdvancedSettings, OpsLogFilterProposal, OpsLogFilterReason, OpsLogFilterRule } from '@/api/admin/ops'

export const localAuthReasons: OpsLogFilterReason[] = ['user_inactive', 'invalid_api_key', 'api_key_required']

export const logFilterPlatforms = [
  { value: 'anthropic', label: 'Anthropic (Claude)' },
  { value: 'openai', label: 'OpenAI' },
  { value: 'gemini', label: 'Gemini' },
  { value: 'antigravity', label: 'Antigravity' }
]

export const legacyLogFilters = [
  { key: 'ignore_count_tokens_errors', label: 'ignoreCountTokensErrors' },
  { key: 'ignore_context_canceled', label: 'ignoreContextCanceled' },
  { key: 'ignore_no_available_accounts', label: 'ignoreNoAvailableAccounts' },
  { key: 'ignore_invalid_api_key_errors', label: 'ignoreInvalidApiKeyErrors' },
  { key: 'ignore_insufficient_balance_errors', label: 'ignoreInsufficientBalanceErrors' }
] as const

export type LegacyLogFilters = Pick<OpsAdvancedSettings, typeof legacyLogFilters[number]['key']>
export interface LogFilterScope {
  group_id?: number | null
  platform?: string
}

function createLogFilterId(): string {
  if (typeof crypto.randomUUID === 'function') return crypto.randomUUID()
  const randomBytes = crypto.getRandomValues(new Uint8Array(16))
  return Array.from(randomBytes, randomByte => randomByte.toString(16).padStart(2, '0')).join('')
}

export function copyLogFilterRule(rule: OpsLogFilterRule): OpsLogFilterRule {
  return { ...rule, status_codes: [...(rule.status_codes ?? [])], keywords: [...(rule.keywords ?? [])] }
}

export function preferredLogFilterScope(scope: LogFilterScope): LogFilterScope {
  if (typeof scope.group_id === 'number' && Number.isInteger(scope.group_id) && scope.group_id > 0) {
    return { group_id: scope.group_id }
  }
  return scope.platform?.trim() ? { platform: scope.platform.trim() } : {}
}

export function isGlobalLogFilter(rule: LogFilterScope): boolean {
  return rule.group_id == null && !rule.platform?.trim()
}

export function isPreIdentityKeyFilter(rule: Pick<OpsLogFilterRule, 'source' | 'reason'>): boolean {
  return rule.source === 'local_auth' && (rule.reason === 'invalid_api_key' || rule.reason === 'api_key_required')
}

export function createLogFilterPreset(reason: OpsLogFilterReason, name: string, scope: LogFilterScope = {}): OpsLogFilterRule {
  return {
    id: createLogFilterId(),
    name,
    enabled: false,
    source: 'local_auth',
    reason,
    match_mode: 'all',
    ...preferredLogFilterScope(isPreIdentityKeyFilter({ source: 'local_auth', reason }) ? { platform: scope.platform } : scope)
  }
}

export function sanitizeLogFilterKeyword(message: string): string {
  return Array.from(message, character => {
    const characterCode = character.charCodeAt(0)
    return characterCode < 32 || characterCode === 127 ? ' ' : character
  }).join('')
    .replace(/(?:bearer\s+|\b(?:sk|sess|ghp|gho|github_pat)[-_])[A-Za-z0-9._~+\/-]+/gi, '[redacted]')
    .replace(/\b(api[_-]?key|access[_-]?token|refresh[_-]?token|authorization|password|secret|cookie)\b["']?\s*[:=]\s*["']?[^\s,;"'}]+/gi, '$1=[redacted]')
    .replace(/https?:\/\/\S+/gi, '[url]')
    .replace(/[A-Za-z0-9_-]{32,}/g, '[redacted]')
    .replace(/\s+/g, ' ')
    .trim()
    .slice(0, 256)
}

export function prefillLogFilterProposal(proposal: OpsLogFilterProposal): OpsLogFilterRule | null {
  if (!proposal.rule) return null
  const rule = copyLogFilterRule(proposal.rule)
  rule.id = createLogFilterId()
  rule.enabled = false
  if (isPreIdentityKeyFilter(rule)) rule.group_id = undefined
  if (rule.source === 'upstream') {
    rule.reason = undefined
    rule.match_mode = 'all'
    rule.keywords = (rule.keywords ?? []).map(sanitizeLogFilterKeyword).filter(Boolean)
  }
  return rule
}

export function validateLogFilterRule(rule: OpsLogFilterRule, globalConfirmed: boolean): string | null {
  if (!/^[A-Za-z0-9-]{1,80}$/.test(rule.id)) return 'invalidId'
  if (!rule.name.trim() || Array.from(rule.name).length > 120) return 'invalidName'
  if (!['local_auth', 'upstream'].includes(rule.source)) return 'invalidSource'
  if (!['all', 'any'].includes(rule.match_mode)) return 'invalidMatchMode'
  if (rule.group_id != null && (!Number.isInteger(rule.group_id) || rule.group_id <= 0)) return 'invalidGroup'
  if (rule.group_id != null && isPreIdentityKeyFilter(rule)) return 'keyReasonGroupScope'
  if (rule.platform != null && !logFilterPlatforms.some(platform => platform.value === rule.platform)) return 'invalidPlatform'
  if ((rule.status_codes ?? []).length > 20 || (rule.status_codes ?? []).some(status => !Number.isInteger(status) || status < 400 || status > 599)) return 'invalidStatuses'
  if ((rule.keywords ?? []).length > 10 || (rule.keywords ?? []).some(keyword => !keyword.trim() || Array.from(keyword).length > 256)) return 'invalidKeywords'
  if (rule.source === 'local_auth' && !localAuthReasons.includes(rule.reason as OpsLogFilterReason)) return 'invalidReason'
  if (rule.source === 'upstream' && (rule.reason || rule.match_mode !== 'all' || !rule.status_codes?.length || !rule.keywords?.length)) return 'unsafeUpstream'
  if (isGlobalLogFilter(rule) && !globalConfirmed) return 'globalConfirmation'
  return null
}

export function mergeLegacyLogFilters(settings: OpsAdvancedSettings, filters: LegacyLogFilters): OpsAdvancedSettings {
  const merged = { ...settings }
  for (const field of legacyLogFilters) merged[field.key] = filters[field.key]
  return merged
}

export function isLogFilterConflict(error: unknown): boolean {
  if (!error || typeof error !== 'object') return false
  const failure = error as { status?: number; response?: { status?: number } }
  return failure.status === 409 || failure.response?.status === 409
}
