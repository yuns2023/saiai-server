/** Thin typed wrappers around the /api/v1 endpoints this app uses. */
import { api } from './client'
import type { OAuthExchangeResponse } from '@/lib/oauth'
import { allowedGroups } from '@/lib/platforms'
import type {
  ApiKey,
  AuthResponse,
  CreateApiKeyRequest,
  Group,
  PaginatedResponse,
  PublicSettings,
  RegisterRequest,
  UpdateApiKeyRequest,
  UsageLog,
  UsageQueryParams,
  UsageStatsResponse,
  User,
  UserAnnouncement,
  UserInviteCode,
  UserInviteOverview
} from '@/types'

export type LoginResult = AuthResponse | { requires_2fa: true; temp_token: string; user_email_masked?: string }

export function needs2FA(r: LoginResult): r is { requires_2fa: true; temp_token: string; user_email_masked?: string } {
  return 'requires_2fa' in r && r.requires_2fa === true
}

export const authApi = {
  async login(email: string, password: string, turnstileToken?: string): Promise<LoginResult> {
    const { data } = await api.post<LoginResult>('/auth/login', { email, password, turnstile_token: turnstileToken })
    return data
  },
  async login2FA(tempToken: string, code: string): Promise<AuthResponse> {
    const { data } = await api.post<AuthResponse>('/auth/login/2fa', { temp_token: tempToken, totp_code: code })
    return data
  },
  async register(req: RegisterRequest): Promise<AuthResponse> {
    const { data } = await api.post<AuthResponse>('/auth/register', req)
    return data
  },
  async me(): Promise<User & { run_mode?: 'standard' | 'simple' }> {
    const { data } = await api.get<User & { run_mode?: 'standard' | 'simple' }>('/auth/me')
    return data
  },
  async logout(refreshToken: string | null): Promise<void> {
    if (!refreshToken) return
    try {
      await api.post('/auth/logout', { refresh_token: refreshToken })
    } catch {
      // Server-side revoke is best effort; the local session is cleared anyway.
    }
  },
  async sendVerifyCode(email: string, turnstileToken?: string): Promise<{ message: string; countdown: number }> {
    const { data } = await api.post<{ message: string; countdown: number }>('/auth/send-verify-code', { email, turnstile_token: turnstileToken })
    return data
  },
  async validatePromoCode(code: string): Promise<{ valid: boolean; bonus_amount?: number; error_code?: string; message?: string }> {
    const { data } = await api.post('/auth/validate-promo-code', { code })
    return data
  },
  async validateInvitationCode(code: string): Promise<{ valid: boolean; error_code?: string }> {
    const { data } = await api.post('/auth/validate-invitation-code', { code })
    return data
  },
  async forgotPassword(email: string, turnstileToken?: string): Promise<{ message: string }> {
    const { data } = await api.post('/auth/forgot-password', { email, turnstile_token: turnstileToken })
    return data
  },
  async resetPassword(email: string, token: string, newPassword: string): Promise<{ message: string }> {
    const { data } = await api.post('/auth/reset-password', { email, token, new_password: newPassword })
    return data
  },
  /** Redeems the one-time callback code; the HttpOnly binder cookie proves this browser started the flow. */
  async exchangeOAuthHandoff(provider: 'google' | 'github', code: string): Promise<OAuthExchangeResponse> {
    const { data } = await api.post<OAuthExchangeResponse>(`/auth/oauth/${provider}/exchange`, { code })
    return data
  },
  /** Finishes an invitation-required first Google / GitHub sign-in (the pending token is single use). */
  async completeOAuthRegistration(provider: 'google' | 'github', pendingToken: string, invitationCode: string): Promise<AuthResponse> {
    const { data } = await api.post<AuthResponse>(`/auth/oauth/${provider}/complete-registration`, {
      pending_oauth_token: pendingToken,
      invitation_code: invitationCode
    })
    return data
  },
  async publicSettings(): Promise<PublicSettings> {
    const { data } = await api.get<PublicSettings>('/settings/public')
    return data
  }
}

export const userApi = {
  async updateProfile(username: string): Promise<User> {
    const { data } = await api.put<User>('/user', { username })
    return data
  },
  async changePassword(oldPassword: string, newPassword: string): Promise<{ message: string }> {
    const { data } = await api.put('/user/password', { old_password: oldPassword, new_password: newPassword })
    return data
  }
}

export const keysApi = {
  async list(page = 1, pageSize = 50, filters: { search?: string; status?: string } = {}): Promise<PaginatedResponse<ApiKey>> {
    const { data } = await api.get<PaginatedResponse<ApiKey>>('/keys', { params: { page, page_size: pageSize, ...filters } })
    return data
  },
  async create(req: CreateApiKeyRequest): Promise<ApiKey> {
    const { data } = await api.post<ApiKey>('/keys', req)
    return data
  },
  async update(id: number, req: UpdateApiKeyRequest): Promise<ApiKey> {
    const { data } = await api.put<ApiKey>(`/keys/${id}`, req)
    return data
  },
  async remove(id: number): Promise<void> {
    await api.delete(`/keys/${id}`)
  }
}

export const groupsApi = {
  /** Groups a key can be bound to, limited to the platforms this UI offers. */
  async available(): Promise<Group[]> {
    const { data } = await api.get<Group[]>('/groups/available')
    return allowedGroups(data || [])
  },
  /** Per-user rate overrides keyed by group id. */
  async rates(): Promise<Record<number, number>> {
    const { data } = await api.get<Record<number, number> | null>('/groups/rates')
    return data || {}
  }
}

export interface ModelTotals {
  model: string
  requests: number
  total_tokens: number
  cost: number
  actual_cost: number
}

export interface DashboardStats {
  total_requests: number
  total_actual_cost: number
  today_requests: number
  today_tokens: number
  today_cost: number
  today_actual_cost: number
  rpm: number
  tpm: number
}

export const usageApi = {
  async query(params: UsageQueryParams, signal?: AbortSignal): Promise<PaginatedResponse<UsageLog>> {
    const { data } = await api.get<PaginatedResponse<UsageLog>>('/usage', { params, signal })
    return data
  },
  async stats(params: { start_date: string; end_date: string; api_key_id?: number }): Promise<UsageStatsResponse> {
    const { data } = await api.get<UsageStatsResponse>('/usage/stats', { params })
    return data
  },
  /** Per-model totals in a date range (feeds the model filter and its totals). */
  async models(params: { start_date: string; end_date: string; api_key_id?: number }): Promise<ModelTotals[]> {
    const { data } = await api.get<{ models: ModelTotals[] }>('/usage/dashboard/models', { params })
    return (data?.models ?? []).filter((m) => !!m.model)
  },
  async dashboard(): Promise<DashboardStats> {
    const { data } = await api.get<DashboardStats>('/usage/dashboard/stats')
    return data
  }
}

/** POST /redeem returns the redeemed code (dto.RedeemCode). */
export interface RedeemResult {
  code: string
  type: string
  value: number
  status: string
  validity_days?: number
  group?: { id: number; name: string }
}

export interface RedeemHistoryItem {
  id: number
  code: string
  type: string
  value: number
  status: string
  used_at: string
  created_at: string
  validity_days?: number
  group?: { id: number; name: string }
}

export const redeemApi = {
  async redeem(code: string): Promise<RedeemResult> {
    const { data } = await api.post<RedeemResult>('/redeem', { code })
    return data
  },
  async history(): Promise<RedeemHistoryItem[]> {
    const { data } = await api.get<RedeemHistoryItem[]>('/redeem/history')
    return data || []
  }
}

export const invitesApi = {
  async overview(page: number, pageSize: number): Promise<UserInviteOverview> {
    const { data } = await api.get<UserInviteOverview>('/invites', { params: { page, page_size: pageSize } })
    return data
  },
  async create(): Promise<UserInviteCode> {
    const { data } = await api.post<UserInviteCode>('/invites')
    return data
  },
  async revoke(code: string): Promise<void> {
    await api.delete(`/invites/${encodeURIComponent(code)}`)
  }
}

export const announcementsApi = {
  async list(): Promise<UserAnnouncement[]> {
    const { data } = await api.get<UserAnnouncement[]>('/announcements')
    return data || []
  },
  async markRead(id: number): Promise<void> {
    await api.post(`/announcements/${id}/read`)
  }
}

export * from './payment'
export * from './cryptoPay'
export * from './hosted'
export * from './redeemPurchase'

/** GET /subscriptions/summary. Limits are omitted (undefined) when the group has none. */
export interface SubscriptionSummary {
  active_count: number
  total_used_usd: number
  subscriptions: Array<{
    id: number
    group_id: number
    group_name: string
    status: string
    five_hour_used_usd?: number
    five_hour_limit_usd?: number
    daily_used_usd?: number
    daily_limit_usd?: number
    weekly_used_usd?: number
    weekly_limit_usd?: number
    monthly_used_usd?: number
    monthly_limit_usd?: number
    expires_at?: string
  }>
}

export const subscriptionsApi = {
  async summary(): Promise<SubscriptionSummary> {
    const { data } = await api.get<SubscriptionSummary>('/subscriptions/summary')
    return data
  }
}
