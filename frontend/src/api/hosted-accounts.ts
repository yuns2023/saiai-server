import { apiClient } from './client'
import type { PaginatedResponse } from '@/types'

export interface HostedGroup {
  id: number
  name: string
  platform: string
}

export interface HostedQuotaWindow {
  key: 'five_hour' | 'seven_day' | 'seven_day_sonnet' | 'seven_day_fable'
  utilization: number
  resets_at: string | null
  expired: boolean
}

export interface HostedAccount {
  id: number
  name: string
  platform: string
  status: 'available' | 'quota_exhausted' | 'paused' | 'expired' | 'rate_limited' | 'temporarily_unavailable' | 'needs_attention'
  updated_at: string | null
  stale: boolean
  windows: HostedQuotaWindow[]
}

export const hostedAccountsAPI = {
  async groups(signal?: AbortSignal): Promise<HostedGroup[]> {
    const { data } = await apiClient.get<HostedGroup[]>('/groups/hosted', { signal })
    return data
  },
  async list(groupID: number, page = 1, signal?: AbortSignal): Promise<PaginatedResponse<HostedAccount>> {
    const { data } = await apiClient.get<PaginatedResponse<HostedAccount>>(`/groups/${groupID}/account-quota`, {
      params: { page, page_size: 20 }, signal
    })
    return data
  }
}
