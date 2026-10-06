import { describe, expect, it, vi } from 'vitest'
import type { OpsLogFilterRule } from '@/api/admin/ops'
import {
  createLogFilterPreset,
  isLogFilterConflict,
  prefillLogFilterProposal,
  sanitizeLogFilterKeyword,
  validateLogFilterRule
} from '../opsLogFilters'

const localRule: OpsLogFilterRule = {
  id: 'local-user-disabled', name: 'Disabled user', enabled: false,
  source: 'local_auth', reason: 'user_inactive', group_id: 7, match_mode: 'all'
}

describe('Ops log filter safety', () => {
  it('creates disabled presets with unique safe IDs and prefers the current group', () => {
    const preset = createLogFilterPreset('user_inactive', 'Disabled user', { group_id: 7, platform: 'anthropic' })
    expect(preset).toMatchObject({ enabled: false, source: 'local_auth', reason: 'user_inactive', group_id: 7 })
    expect(preset.platform).toBeUndefined()
    expect(preset.id).toMatch(/^[A-Za-z0-9-]{1,80}$/)
    expect(createLogFilterPreset('user_inactive', 'Disabled user').id).not.toBe(preset.id)
  })

  it('uses platform scope when there is no current group', () => {
    expect(createLogFilterPreset('invalid_api_key', 'Invalid key', { platform: ' openai ' }))
      .toMatchObject({ enabled: false, platform: 'openai' })
  })

  it.each(['invalid_api_key', 'api_key_required'] as const)('never creates or proposes a group-scoped %s rule', reason => {
    const preset = createLogFilterPreset(reason, 'Key rejected', { group_id: 7, platform: 'anthropic' })
    expect(preset).toMatchObject({ enabled: false, source: 'local_auth', reason, platform: 'anthropic' })
    expect(preset.group_id).toBeUndefined()
    expect(validateLogFilterRule({ ...localRule, reason }, true)).toBe('keyReasonGroupScope')
    const proposed = prefillLogFilterProposal({
      rule: { ...localRule, reason, platform: 'anthropic' }, verified: false, requires_global_confirmation: false
    })
    expect(proposed).toMatchObject({ enabled: false, source: 'local_auth', reason, platform: 'anthropic' })
    expect(proposed?.group_id).toBeUndefined()
  })

  it('uses cryptographic random IDs when randomUUID is unavailable on an HTTP admin origin', () => {
    vi.stubGlobal('crypto', { getRandomValues: (bytes: Uint8Array) => bytes.fill(17) })
    try {
      const preset = createLogFilterPreset('invalid_api_key', 'Invalid key')
      expect(preset.id).toBe('11'.repeat(16))
      expect(preset.id).toMatch(/^[A-Za-z0-9-]{1,80}$/)
    } finally {
      vi.unstubAllGlobals()
    }
  })

  it('requires explicit confirmation for an unscoped rule even when disabled', () => {
    const globalRule = { ...localRule, group_id: undefined }
    expect(validateLogFilterRule(globalRule, false)).toBe('globalConfirmation')
    expect(validateLogFilterRule(globalRule, true)).toBeNull()
    expect(validateLogFilterRule(localRule, false)).toBeNull()
  })

  it('preserves backend local source and reason for unverified historical suggestions', () => {
    const original = { ...localRule, enabled: true }
    const candidate = prefillLogFilterProposal({ rule: original, verified: false, requires_global_confirmation: false })
    expect(candidate).toMatchObject({ enabled: false, source: 'local_auth', reason: 'user_inactive', group_id: 7 })
    expect(candidate?.id).not.toBe(original.id)
    expect(original.enabled).toBe(true)
    expect(prefillLogFilterProposal({ rule: null, verified: false, requires_global_confirmation: false })).toBeNull()
  })

  it('keeps upstream suggestions upstream, disabled, sanitized and all-condition', () => {
    const upstream = { ...localRule, source: 'upstream' as const, match_mode: 'any' as const, status_codes: [403], keywords: ['  denied\nBearer secret-token  '] }
    const candidate = prefillLogFilterProposal({ rule: upstream, verified: false, requires_global_confirmation: false })
    expect(candidate).toMatchObject({ enabled: false, source: 'upstream', match_mode: 'all', keywords: ['denied [redacted]'] })
    expect(candidate?.reason).toBeUndefined()
    expect(upstream.keywords[0]).toContain('secret-token')
  })

  it('redacts credential and URL shapes and trims keyword previews to 256 characters', () => {
    const sanitized = sanitizeLogFilterKeyword('Denied api_key=example-secret sk-example-secret https://example.test/?token=secret\u0000')
    expect(sanitized).not.toContain('example-secret')
    expect(sanitized).not.toContain('token=secret')
    expect(sanitized).not.toContain('\u0000')
    expect(sanitizeLogFilterKeyword('Failure '.repeat(100))).toHaveLength(256)
  })

  it.each([
    [{ source: 'local_auth', reason: undefined }, 'invalidReason'],
    [{ id: 'bad_id' }, 'invalidId'],
    [{ id: 'valid'.repeat(21) }, 'invalidId'],
    [{ name: ' '.repeat(2) }, 'invalidName'],
    [{ name: '名'.repeat(121) }, 'invalidName'],
    [{ group_id: 0 }, 'invalidGroup'],
    [{ group_id: 1.5 }, 'invalidGroup'],
    [{ status_codes: [399] }, 'invalidStatuses'],
    [{ status_codes: [600] }, 'invalidStatuses'],
    [{ status_codes: [400.5] }, 'invalidStatuses'],
    [{ status_codes: Array(21).fill(400) }, 'invalidStatuses'],
    [{ platform: 'unknown-platform' }, 'invalidPlatform'],
    [{ keywords: ['word'.repeat(65)] }, 'invalidKeywords'],
    [{ keywords: Array(11).fill('word') }, 'invalidKeywords'],
    [{ source: 'upstream', status_codes: [403], keywords: [] }, 'unsafeUpstream'],
    [{ source: 'upstream', status_codes: [], keywords: ['denied'] }, 'unsafeUpstream'],
    [{ source: 'upstream', status_codes: [403], keywords: ['denied'], match_mode: 'any' }, 'unsafeUpstream']
  ])('rejects invalid or unsafe rule fields %j', (changes, expected) => {
    expect(validateLogFilterRule({ ...localRule, ...changes } as OpsLogFilterRule, true)).toBe(expected)
  })

  it('accepts safe upstream conditions and both boundary statuses', () => {
    expect(validateLogFilterRule({ ...localRule, source: 'upstream', reason: undefined, status_codes: [400, 599], keywords: ['denied'] }, true)).toBeNull()
  })

  it('recognizes normalized and Axios conflict errors', () => {
    expect(isLogFilterConflict({ status: 409 })).toBe(true)
    expect(isLogFilterConflict({ response: { status: 409 } })).toBe(true)
    expect(isLogFilterConflict({ status: 500 })).toBe(false)
    expect(isLogFilterConflict(null)).toBe(false)
  })
})
