/**
 * Hosted self-onboarding applications (admin): users submit their own Claude
 * setup-token account; an administrator approves (creating the account in the
 * owner's hosted group, always behind a proxy) or rejects with a reason.
 */
import { apiClient } from '../client'
import type { PaginatedResponse } from '@/types'

export type HostedApplicationStatus = 'pending' | 'approved' | 'rejected' | 'cancelled' | 'expired'

/** What approve / reject return as `application` (the user-facing shape). */
export interface HostedApplication {
  id: number
  status: HostedApplicationStatus | string
  display_name: string
  upstream_email: string
  /** "socks5h://host:port" or "" when the applicant gave no proxy. Never carries credentials. */
  proxy_display: string
  reject_reason: string
  account_id: number | null
  created_at: string
  reviewed_at: string | null
}

/** List rows: the application plus admin-only applicant and proxy details. */
export interface AdminHostedApplication extends HostedApplication {
  user_id: number
  user_email: string
  proxy_protocol: string
  proxy_host: string
  proxy_port: number
  has_proxy_auth: boolean
}

export type ApproveProxyMode = 'existing' | 'submitted'

export interface ApproveHostedApplicationRequest {
  proxy_mode: ApproveProxyMode
  /** Required for proxy_mode "existing". */
  proxy_id?: number
  concurrency: number
  priority: number
  carpool_device_limit: number
  carpool_admissions_per_cycle: number
  carpool_interval_hours: number
  /** Optional sharing 套餐: the approval also grants sharing (the account starts as 自用). Omitted = no sharing. */
  market_plan?: string
}

export interface ApproveHostedApplicationResult {
  application: AdminHostedApplication
  account_id: number
  group_id: number
}

export interface ListHostedApplicationsParams {
  /** '' = every status. */
  status?: HostedApplicationStatus | ''
}

export const hostedApplicationsAPI = {
  async list(page: number, pageSize: number, params: ListHostedApplicationsParams = {}): Promise<PaginatedResponse<AdminHostedApplication>> {
    const query: Record<string, string | number> = { page, page_size: pageSize }
    if (params.status) query.status = params.status
    const { data } = await apiClient.get<PaginatedResponse<AdminHostedApplication>>('/admin/hosted-applications', { params: query })
    return { ...data, items: data?.items ?? [] }
  },
  async approve(id: number, body: ApproveHostedApplicationRequest): Promise<ApproveHostedApplicationResult> {
    const { data } = await apiClient.post<ApproveHostedApplicationResult>(`/admin/hosted-applications/${id}/approve`, body)
    return data
  },
  async reject(id: number, reason: string): Promise<AdminHostedApplication> {
    const { data } = await apiClient.post<AdminHostedApplication>(`/admin/hosted-applications/${id}/reject`, { reason })
    return data
  }
}

/** Approve-dialog form: numbers as typed (v-model.number may yield '' for an empty field). */
export interface ApproveForm {
  proxy_mode: ApproveProxyMode
  proxy_id: number | null
  concurrency: number | ''
  priority: number | ''
  carpool_device_limit: number | ''
  carpool_admissions_per_cycle: number | ''
  carpool_interval_hours: number | ''
  /** '' = 不开通共享. */
  market_plan: string
}

/**
 * Bounds of the approve request and of the settings defaults, as the backend
 * enforces them (service/setting_hosted_self_onboard.go); admissions per cycle
 * is also at most the device limit.
 */
export const APPROVE_LIMITS = {
  concurrency: { min: 1, max: 100 },
  priority: { min: 0, max: 10000 },
  carpool_device_limit: { min: 1, max: 32 },
  carpool_admissions_per_cycle: { min: 0, max: 32 },
  carpool_interval_hours: { min: 1, max: 720 }
} as const

export const HOSTED_FEE_RATE_MAX = 10

/** 0..10 with at most 4 decimals (groups.rate_multiplier is decimal(10,4)). */
export function validHostedFeeRate(v: unknown): v is number {
  if (typeof v !== 'number' || !Number.isFinite(v) || v < 0 || v > HOSTED_FEE_RATE_MAX) return false
  return Math.abs(v * 10000 - Math.round(v * 10000)) <= 1e-7
}

export type ApproveField = keyof typeof APPROVE_LIMITS | 'proxy'

function whole(v: unknown, min: number, max: number): boolean {
  return typeof v === 'number' && Number.isInteger(v) && v >= min && v <= max
}

/**
 * Fields that block approval. A proxy is always required: "submitted" needs
 * the applicant's proxy, "existing" needs a chosen proxy. There is no direct
 * (proxy-less) option.
 */
export function approveProblems(form: ApproveForm, hasSubmittedProxy: boolean): ApproveField[] {
  const out: ApproveField[] = []
  if (form.proxy_mode === 'submitted' ? !hasSubmittedProxy : !(typeof form.proxy_id === 'number' && form.proxy_id > 0)) out.push('proxy')
  for (const key of Object.keys(APPROVE_LIMITS) as Array<keyof typeof APPROVE_LIMITS>) {
    const { min, max } = APPROVE_LIMITS[key]
    if (!whole(form[key], min, max)) out.push(key)
  }
  if (!out.includes('carpool_admissions_per_cycle') && !out.includes('carpool_device_limit') && Number(form.carpool_admissions_per_cycle) > Number(form.carpool_device_limit)) {
    out.push('carpool_admissions_per_cycle')
  }
  return out
}

/** Request body for a form without problems. */
export function approvePayload(form: ApproveForm): ApproveHostedApplicationRequest {
  const body: ApproveHostedApplicationRequest = {
    proxy_mode: form.proxy_mode,
    concurrency: Number(form.concurrency),
    priority: Number(form.priority),
    carpool_device_limit: Number(form.carpool_device_limit),
    carpool_admissions_per_cycle: Number(form.carpool_admissions_per_cycle),
    carpool_interval_hours: Number(form.carpool_interval_hours)
  }
  if (form.proxy_mode === 'existing' && form.proxy_id) body.proxy_id = form.proxy_id
  // Only a chosen 套餐 is sent: none means the account cannot be shared until an administrator sets one.
  if (form.market_plan) body.market_plan = form.market_plan
  return body
}

export default hostedApplicationsAPI
