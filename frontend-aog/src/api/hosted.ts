/**
 * Hosted accounts I own (/api/v1/hosted). Upstream accounts that belong to
 * this user, now (`current`) or in the past (history only). Other users'
 * calls on them are reported as usage only, never with their identity.
 */
import { api } from './client'

export type HostedAccountStatus =
  | 'available'
  | 'needs_attention'
  | 'paused'
  | 'expired'
  | 'rate_limited'
  | 'temporarily_unavailable'

/** One rate-limit window; keys seen today: five_hour, seven_day, seven_day_sonnet, seven_day_opus. */
export interface HostedWindow {
  key: string
  /** Percent 0–100, may exceed 100. */
  utilization: number | null
  resets_at: string | null
  expired: boolean
  used_cost_usd: number | null
  estimated_capacity_usd: number | null
  estimate_sampled_at: string | null
}

export interface HostedUsageSummary {
  self_requests: number
  self_standard_cost: number
  /** Hosting service fee charged to me for my own requests (absent from older servers). */
  self_service_fee?: number | null
  others_requests: number
  others_standard_cost: number
}

export interface OwnedAccount {
  account_id: number
  /** false: it was mine in the past; status/type/windows are then null/empty. */
  current: boolean
  display_name: string
  platform: string | null
  type: string | null
  status: HostedAccountStatus | string | null
  /** Utilization sample time. */
  updated_at: string | null
  stale: boolean
  windows: HostedWindow[]
  last_24h: HostedUsageSummary | null
  last_7d: HostedUsageSummary | null
  /** I paused it (no new requests). Absent from servers without self-onboarding. */
  owner_paused?: boolean
  /** I asked for it to be taken down; an administrator removes it. Not revocable. */
  withdraw_requested?: boolean
}

/** Self-onboarding: submit my own Claude account (setup-token) for hosting; an administrator approves. */
export interface SelfOnboardConfig {
  enabled: boolean
  /** false: the site has no exchange proxy, so my own proxy is required. */
  exchange_proxy_configured: boolean
  max_pending: number
}

export interface SelfOnboardAuthUrl {
  session_id: string
  auth_url: string
  /** RFC3339. */
  expires_at: string
}

export type ProxyProtocol = 'http' | 'https' | 'socks5' | 'socks5h'

export interface SelfOnboardProxy {
  protocol: ProxyProtocol
  host: string
  port: number
  username?: string
  password?: string
}

export interface SelfOnboardSubmit {
  session_id: string
  /** The `code#state` string claude.ai shows after authorising. */
  code: string
  display_name: string
  proxy?: SelfOnboardProxy
}

export type ApplicationStatus = 'pending' | 'approved' | 'rejected' | 'cancelled' | 'expired'

export interface HostedApplication {
  id: number
  status: ApplicationStatus | string
  display_name: string
  upstream_email: string
  /** "socks5h://host:port", or "" when I did not submit a proxy. Never carries credentials. */
  proxy_display: string
  reject_reason: string
  account_id: number | null
  created_at: string
  reviewed_at: string | null
}

export interface OwnerState {
  id: number
  owner_paused: boolean
  withdraw_requested: boolean
}

export interface OwnedUsageItem {
  created_at: string
  model: string
  input_tokens: number
  output_tokens: number
  cache_creation_tokens: number
  cache_read_tokens: number
  standard_cost: number
  /** false: another user's call on my account — usage only. */
  is_self: boolean
  api_key_name: string | null
  /** Only for my own calls. */
  charged: number | null
}

export interface OwnedUsagePage {
  items: OwnedUsageItem[]
  next_cursor: string | null
}

export interface OwnedUsageQuery {
  /** RFC3339 (UTC); half-open [start, end), at most 31 days. */
  start: string
  end: string
  /** 1..100, server default 50. */
  limit?: number
  /** Opaque cursor from the previous page. */
  cursor?: string | null
}

export const hostedApi = {
  async listOwnedAccounts(): Promise<OwnedAccount[]> {
    const { data } = await api.get<{ items: OwnedAccount[] | null }>('/hosted/owned-accounts')
    return (data?.items ?? []).map((a) => ({ ...a, windows: a.windows ?? [] }))
  },
  async listOwnedAccountUsage(accountId: number, query: OwnedUsageQuery, signal?: AbortSignal): Promise<OwnedUsagePage> {
    const params: Record<string, string | number> = { start: query.start, end: query.end }
    if (query.limit != null) params.limit = query.limit
    if (query.cursor) params.cursor = query.cursor
    const { data } = await api.get<OwnedUsagePage>(`/hosted/owned-accounts/${encodeURIComponent(String(accountId))}/usage`, { params, signal })
    return { items: data?.items ?? [], next_cursor: data?.next_cursor || null }
  },

  /* ---------- Self-onboarding ---------- */
  async selfOnboardConfig(): Promise<SelfOnboardConfig> {
    const { data } = await api.get<SelfOnboardConfig>('/hosted/self-onboard/config')
    return {
      enabled: data?.enabled === true,
      exchange_proxy_configured: data?.exchange_proxy_configured === true,
      max_pending: typeof data?.max_pending === 'number' ? data.max_pending : 3
    }
  },
  async createAuthUrl(): Promise<SelfOnboardAuthUrl> {
    const { data } = await api.post<SelfOnboardAuthUrl>('/hosted/applications/auth-url')
    return data
  },
  async submitApplication(req: SelfOnboardSubmit): Promise<HostedApplication> {
    const { data } = await api.post<HostedApplication>('/hosted/applications', req)
    return data
  },
  async listApplications(): Promise<HostedApplication[]> {
    const { data } = await api.get<{ items: HostedApplication[] | null }>('/hosted/applications')
    return data?.items ?? []
  },
  async cancelApplication(id: number): Promise<HostedApplication> {
    const { data } = await api.post<HostedApplication>(`/hosted/applications/${encodeURIComponent(String(id))}/cancel`)
    return data
  },
  async setOwnerState(accountId: number, action: 'pause' | 'resume' | 'withdraw'): Promise<OwnerState> {
    const { data } = await api.post<OwnerState>(`/hosted/owned-accounts/${encodeURIComponent(String(accountId))}/${action}`)
    return data
  }
}
