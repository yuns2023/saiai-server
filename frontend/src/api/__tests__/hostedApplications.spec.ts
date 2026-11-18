import { beforeEach, describe, expect, it, vi } from 'vitest'

const http = vi.hoisted(() => ({ get: vi.fn(), post: vi.fn() }))
vi.mock('@/api/client', () => ({ apiClient: http }))

import { approvePayload, approveProblems, hostedApplicationsAPI, type ApproveForm } from '@/api/admin/hostedApplications'

function form(over: Partial<ApproveForm> = {}): ApproveForm {
  return {
    proxy_mode: 'existing',
    proxy_id: 4,
    concurrency: 3,
    priority: 1,
    carpool_device_limit: 5,
    carpool_admissions_per_cycle: 1,
    carpool_interval_hours: 24,
    ...over
  }
}

describe('approveProblems', () => {
  it('accepts a complete form', () => {
    expect(approveProblems(form(), false)).toEqual([])
    expect(approveProblems(form({ proxy_mode: 'submitted', proxy_id: null }), true)).toEqual([])
  })

  it('never allows approval without a proxy', () => {
    expect(approveProblems(form({ proxy_id: null }), true)).toEqual(['proxy'])
    expect(approveProblems(form({ proxy_id: 0 }), true)).toEqual(['proxy'])
    // "submitted" without a submitted proxy is not a way around it.
    expect(approveProblems(form({ proxy_mode: 'submitted', proxy_id: null }), false)).toEqual(['proxy'])
  })

  it('checks every number against its range', () => {
    expect(approveProblems(form({ concurrency: 0 }), false)).toEqual(['concurrency'])
    expect(approveProblems(form({ priority: '' }), false)).toEqual(['priority'])
    expect(approveProblems(form({ carpool_device_limit: 33 }), false)).toEqual(['carpool_device_limit'])
    expect(approveProblems(form({ carpool_interval_hours: 721 }), false)).toEqual(['carpool_interval_hours'])
    expect(approveProblems(form({ carpool_interval_hours: 1.5 }), false)).toEqual(['carpool_interval_hours'])
    expect(approveProblems(form({ carpool_admissions_per_cycle: 6, carpool_device_limit: 5 }), false)).toEqual(['carpool_admissions_per_cycle'])
    expect(approveProblems(form({ carpool_admissions_per_cycle: 0 }), false)).toEqual([])
  })
})

describe('approvePayload', () => {
  it('sends proxy_id only for an existing proxy', () => {
    expect(approvePayload(form())).toEqual({
      proxy_mode: 'existing',
      proxy_id: 4,
      concurrency: 3,
      priority: 1,
      carpool_device_limit: 5,
      carpool_admissions_per_cycle: 1,
      carpool_interval_hours: 24
    })
    expect(approvePayload(form({ proxy_mode: 'submitted' }))).not.toHaveProperty('proxy_id')
  })
})

describe('hostedApplicationsAPI', () => {
  beforeEach(() => {
    http.get.mockReset()
    http.post.mockReset()
  })

  it('lists with the status filter only when set', async () => {
    http.get.mockResolvedValue({ data: { items: null, total: 0, page: 1, page_size: 20, pages: 0 } })
    expect((await hostedApplicationsAPI.list(1, 20, { status: '' })).items).toEqual([])
    expect(http.get).toHaveBeenLastCalledWith('/admin/hosted-applications', { params: { page: 1, page_size: 20 } })
    await hostedApplicationsAPI.list(2, 50, { status: 'pending' })
    expect(http.get).toHaveBeenLastCalledWith('/admin/hosted-applications', { params: { page: 2, page_size: 50, status: 'pending' } })
  })

  it('posts approve and reject', async () => {
    http.post.mockResolvedValue({ data: {} })
    await hostedApplicationsAPI.approve(9, approvePayload(form()))
    expect(http.post).toHaveBeenLastCalledWith('/admin/hosted-applications/9/approve', approvePayload(form()))
    await hostedApplicationsAPI.reject(9, '代理不可用')
    expect(http.post).toHaveBeenLastCalledWith('/admin/hosted-applications/9/reject', { reason: '代理不可用' })
  })
})
