import { beforeEach, describe, expect, it, vi } from 'vitest'
import { opsAPI, type OpsLogFilterConfig } from '../admin/ops'

const client = vi.hoisted(() => ({ get: vi.fn(), put: vi.fn() }))
vi.mock('../client', () => ({ apiClient: client }))

const config: OpsLogFilterConfig = { rules: [], revision: 'revision-1', counts: {}, count_scope: 'process' }

describe('Ops log filter API', () => {
  beforeEach(() => vi.clearAllMocks())

  it('loads config and passes the cancellation signal', async () => {
    client.get.mockResolvedValue({ data: config })
    const signal = new AbortController().signal
    expect(await opsAPI.getLogFilters({ signal })).toEqual(config)
    expect(client.get).toHaveBeenCalledWith('/admin/ops/log-filters', { signal })
  })

  it('sends only rules and the required revision and returns the new config', async () => {
    const next = { ...config, revision: 'revision-2' }
    client.put.mockResolvedValue({ data: next })
    expect(await opsAPI.updateLogFilters({ rules: [], revision: config.revision })).toEqual(next)
    expect(client.put).toHaveBeenCalledWith('/admin/ops/log-filters', { rules: [], revision: 'revision-1' }, {})
  })

  it('loads the backend proposal without using retry or provider endpoints', async () => {
    const proposal = { rule: null, verified: false, requires_global_confirmation: false }
    client.get.mockResolvedValue({ data: proposal })
    expect(await opsAPI.getLogFilterProposal(42)).toEqual(proposal)
    expect(client.get).toHaveBeenCalledWith('/admin/ops/errors/42/log-filter-proposal', {})
    expect(client.put).not.toHaveBeenCalled()
  })

  it('propagates optimistic conflicts without retrying or dropping the revision', async () => {
    const conflict = { status: 409, message: 'Conflict' }
    client.put.mockRejectedValue(conflict)
    await expect(opsAPI.updateLogFilters({ rules: [], revision: 'old-revision' })).rejects.toBe(conflict)
    expect(client.put).toHaveBeenCalledTimes(1)
  })
})
