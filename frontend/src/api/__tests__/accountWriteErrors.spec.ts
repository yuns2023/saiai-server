import { describe, expect, it, vi } from 'vitest'

const http = vi.hoisted(() => ({ post: vi.fn(), put: vi.fn() }))
vi.mock('@/api/client', () => ({ apiClient: http }))
vi.mock('@/i18n', () => ({ i18n: { global: { t: (key: string) => `T:${key}` } } }))

import { create, update } from '@/api/admin/accounts'

describe('account write errors', () => {
  it('localizes the hosted self-onboarding reasons and keeps the reason', async () => {
    http.post.mockRejectedValue({ status: 409, reason: 'UPSTREAM_ACCOUNT_ALREADY_EXISTS', message: 'another live account already uses this upstream account' })
    await expect(create({} as never)).rejects.toEqual({
      status: 409,
      reason: 'UPSTREAM_ACCOUNT_ALREADY_EXISTS',
      message: 'T:admin.accounts.errors.upstreamAccountAlreadyExists'
    })
    http.put.mockRejectedValue({ status: 400, reason: 'SELF_ONBOARDED_ACCOUNT_REQUIRES_PROXY', message: 'x' })
    await expect(update(3, {} as never)).rejects.toMatchObject({ message: 'T:admin.accounts.errors.selfOnboardedRequiresProxy' })
    http.put.mockRejectedValue({ status: 400, reason: 'SELF_ONBOARDED_ACCOUNT_TYPE_LOCKED', message: 'x' })
    await expect(update(3, {} as never)).rejects.toMatchObject({ message: 'T:admin.accounts.errors.selfOnboardedTypeLocked' })
  })

  it('passes other errors through unchanged', async () => {
    const err = { status: 500, message: 'boom' }
    http.put.mockRejectedValue(err)
    await expect(update(3, {} as never)).rejects.toBe(err)
  })
})
