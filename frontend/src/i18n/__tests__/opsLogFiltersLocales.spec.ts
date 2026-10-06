import { describe, expect, it } from 'vitest'
import en from '../locales/en'
import zh from '../locales/zh'

describe('Ops log filtering locales', () => {
  it('provides matching English and Chinese keys for the manager and validation', () => {
    expect(Object.keys(en.admin.ops.logFilters).sort()).toEqual(Object.keys(zh.admin.ops.logFilters).sort())
    expect(Object.keys(en.admin.ops.logFilters.validation).sort()).toEqual(Object.keys(zh.admin.ops.logFilters.validation).sort())
    expect(Object.keys(en.admin.ops.logFilters.reasons).sort()).toEqual(Object.keys(zh.admin.ops.logFilters.reasons).sort())
    expect(zh.admin.ops.logFilters.title).toBe('日志过滤')
    expect(zh.admin.ops.logFilters.reasons.user_inactive).toBe('本站用户已禁用')
    expect(zh.admin.ops.logFilters.reasons.invalid_api_key).toBe('本站 Key 无效')
  })

  it('explicitly describes future-only, unrecoverable details and process-only restartable counts', () => {
    expect(en.admin.ops.logFilters.futureOnly).toContain('cannot be recovered')
    expect(en.admin.ops.logFilters.countScope).toContain('not cluster totals')
    expect(en.admin.ops.logFilters.countScope).toContain('reset on restart')
    expect(zh.admin.ops.logFilters.futureOnly).toContain('无法恢复')
    expect(zh.admin.ops.logFilters.countScope).toContain('不是集群总数')
    expect(zh.admin.ops.logFilters.countScope).toContain('重启后清零')
  })

  it('clarifies that the legacy invalid-key filter only skips trusted local SAIAI Key rejections', () => {
    expect(en.admin.ops.settings.ignoreInvalidApiKeyErrorsHint).toContain('SAIAI Key')
    expect(en.admin.ops.settings.ignoreInvalidApiKeyErrorsHint).toContain('explicitly marked as local')
    expect(en.admin.ops.settings.ignoreInvalidApiKeyErrorsHint).toContain('upstream authentication failures are retained')
    expect(zh.admin.ops.settings.ignoreInvalidApiKeyErrorsHint).toContain('明确标记为本站拒绝')
    expect(zh.admin.ops.settings.ignoreInvalidApiKeyErrorsHint).toContain('SAIAI Key')
    expect(zh.admin.ops.settings.ignoreInvalidApiKeyErrorsHint).toContain('上游认证失败仍会保留')
  })

  it('explains why missing or invalid Keys cannot be scoped by group', () => {
    expect(en.admin.ops.logFilters.keyReasonScopeHint).toContain('group is unknown before the Key is verified')
    expect(zh.admin.ops.logFilters.keyReasonScopeHint).toContain('验证 Key 前无法确定分组')
    expect(zh.admin.ops.logFilters.keyReasonScopeHint).toContain('请按平台或全站过滤')
  })
})
