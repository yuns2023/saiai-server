/**
 * Usage tracking API endpoints
 * Handles usage logs and statistics retrieval
 */

import { apiClient } from './client'
import type {
  UsageLog,
  UsageQueryParams,
  UsageStatsResponse,
  PaginatedResponse,
  TrendDataPoint,
  ModelStat
} from '@/types'

// ==================== Dashboard Types ====================

export interface UserDashboardStats {
  total_api_keys: number
  active_api_keys: number
  total_requests: number
  total_input_tokens: number
  total_output_tokens: number
  total_cache_creation_tokens: number
  total_cache_read_tokens: number
  total_tokens: number
  total_cost: number // 标准计费
  total_actual_cost: number // 实际扣除
  today_requests: number
  today_input_tokens: number
  today_output_tokens: number
  today_cache_creation_tokens: number
  today_cache_read_tokens: number
  today_tokens: number
  today_cost: number // 今日标准计费
  today_actual_cost: number // 今日实际扣除
  average_duration_ms: number
  rpm: number // 近5分钟平均每分钟请求数
  tpm: number // 近5分钟平均每分钟Token数
}

export interface TrendParams {
  start_date?: string
  end_date?: string
  granularity?: 'day' | 'hour' | '15m'
  api_key_id?: number
  group_id?: number
  timezone?: string
}

export interface TrendResponse {
  trend: TrendDataPoint[]
  start_date: string
  end_date: string
  granularity: string
}

export interface ModelStatsResponse {
  models: ModelStat[]
  start_date: string
  end_date: string
}

/**
 * List usage logs with optional filters
 * @param page - Page number (default: 1)
 * @param pageSize - Items per page (default: 20)
 * @param apiKeyId - Filter by API key ID
 * @returns Paginated list of usage logs
 */
export async function list(
  page: number = 1,
  pageSize: number = 20,
  apiKeyId?: number
): Promise<PaginatedResponse<UsageLog>> {
  const params: UsageQueryParams = {
    page,
    page_size: pageSize
  }

  if (apiKeyId !== undefined) {
    params.api_key_id = apiKeyId
  }

  const { data } = await apiClient.get<PaginatedResponse<UsageLog>>('/usage', {
    params
  })
  return data
}

/**
 * Get usage logs with advanced query parameters
 * @param params - Query parameters for filtering and pagination
 * @returns Paginated list of usage logs
 */
export async function query(
  params: UsageQueryParams,
  config: { signal?: AbortSignal } = {}
): Promise<PaginatedResponse<UsageLog>> {
  const { data } = await apiClient.get<PaginatedResponse<UsageLog>>('/usage', {
    ...config,
    params
  })
  return data
}

/**
 * Get usage statistics for a specific period
 * @param period - Time period ('today', 'week', 'month', 'year')
 * @param apiKeyId - Optional API key ID filter
 * @returns Usage statistics
 */
export async function getStats(
  period: string = 'today',
  apiKeyId?: number
): Promise<UsageStatsResponse> {
  const params: Record<string, unknown> = { period }

  if (apiKeyId !== undefined) {
    params.api_key_id = apiKeyId
  }

  const { data } = await apiClient.get<UsageStatsResponse>('/usage/stats', {
    params
  })
  return data
}

/**
 * Get usage statistics for a date range
 * @param startDate - Start date (YYYY-MM-DD format)
 * @param endDate - End date (YYYY-MM-DD format)
 * @param apiKeyId - Optional API key ID filter
 * @param groupId - Optional final (actual) group filter
 * @returns Usage statistics
 */
export async function getStatsByDateRange(
  startDate: string,
  endDate: string,
  apiKeyId?: number,
  timezone?: string,
  groupId?: number
): Promise<UsageStatsResponse> {
  const params: Record<string, unknown> = {
    start_date: startDate,
    end_date: endDate
  }

  if (apiKeyId !== undefined) {
    params.api_key_id = apiKeyId
  }
  if (groupId !== undefined) {
    params.group_id = groupId
  }
  if (timezone) {
    params.timezone = timezone
  }

  const { data } = await apiClient.get<UsageStatsResponse>('/usage/stats', {
    params
  })
  return data
}

/**
 * Get usage by date range
 * @param startDate - Start date (YYYY-MM-DD format)
 * @param endDate - End date (YYYY-MM-DD format)
 * @param apiKeyId - Optional API key ID filter
 * @param groupId - Optional final (actual) group filter
 * @returns Usage logs within date range
 */
export async function getByDateRange(
  startDate: string,
  endDate: string,
  apiKeyId?: number,
  timezone?: string,
  groupId?: number
): Promise<PaginatedResponse<UsageLog>> {
  const params: UsageQueryParams = {
    start_date: startDate,
    end_date: endDate,
    page: 1,
    page_size: 100
  }

  if (apiKeyId !== undefined) {
    params.api_key_id = apiKeyId
  }
  if (groupId !== undefined) {
    params.group_id = groupId
  }
  if (timezone) {
    params.timezone = timezone
  }

  const { data } = await apiClient.get<PaginatedResponse<UsageLog>>('/usage', {
    params
  })
  return data
}

/**
 * Get detailed usage log by ID
 * @param id - Usage log ID
 * @returns Usage log details
 */
export async function getById(id: number): Promise<UsageLog> {
  const { data } = await apiClient.get<UsageLog>(`/usage/${id}`)
  return data
}

export interface UsageGroupOption {
  id: number
  name: string
}

/**
 * List the final (actual) groups found in the current user's usage within a
 * date range, most-used first. Includes groups the user can no longer use.
 */
export async function getUsageGroups(
  startDate: string,
  endDate: string,
  timezone?: string
): Promise<UsageGroupOption[]> {
  const params: Record<string, unknown> = { start_date: startDate, end_date: endDate }
  if (timezone) {
    params.timezone = timezone
  }
  const { data } = await apiClient.get<UsageGroupOption[]>('/usage/groups', { params })
  return data
}

// ==================== Dashboard API ====================

/**
 * Get user dashboard statistics
 * @returns Dashboard statistics for current user
 */
export async function getDashboardStats(
  apiKeyId?: number,
  groupId?: number
): Promise<UserDashboardStats> {
  const params =
    apiKeyId === undefined && groupId === undefined
      ? undefined
      : {
          ...(apiKeyId === undefined ? {} : { api_key_id: apiKeyId }),
          ...(groupId === undefined ? {} : { group_id: groupId })
        }
  const { data } = await apiClient.get<UserDashboardStats>('/usage/dashboard/stats', { params })
  return data
}

/**
 * Get user usage trend data
 * @param params - Query parameters for filtering
 * @returns Usage trend data for current user
 */
export async function getDashboardTrend(params?: TrendParams): Promise<TrendResponse> {
  const { data } = await apiClient.get<TrendResponse>('/usage/dashboard/trend', { params })
  return data
}

/**
 * Get user model usage statistics
 * @param params - Query parameters for filtering
 * @returns Model usage statistics for current user
 */
export async function getDashboardModels(params?: {
  start_date?: string
  end_date?: string
  api_key_id?: number
  group_id?: number
  timezone?: string
}): Promise<ModelStatsResponse> {
  const { data } = await apiClient.get<ModelStatsResponse>('/usage/dashboard/models', { params })
  return data
}

export type APIKeyUsageBreakdownSort =
  | 'actual_cost_desc'
  | 'requests_desc'
  | 'tokens_desc'
  | 'last_used_desc'
  | 'name_asc'

export interface APIKeyUsageBreakdownItem {
  api_key_id: number
  key_name: string
  status: string
  last_used_at: string | null
  requests: number
  input_tokens: number
  output_tokens: number
  cache_creation_tokens: number
  cache_read_tokens: number
  total_tokens: number
  total_cost: number
  actual_cost: number
  actual_cost_share: number
}

export interface APIKeyUsageBreakdownSummary {
  requests: number
  total_tokens: number
  total_cost: number
  actual_cost: number
}

export interface APIKeyUsageBreakdownResponse {
  items: APIKeyUsageBreakdownItem[]
  total: number
  page: number
  page_size: number
  pages: number
  summary: APIKeyUsageBreakdownSummary
  start_date: string
  end_date: string
  sort: APIKeyUsageBreakdownSort
}

export interface APIKeyUsageBreakdownParams {
  start_date?: string
  end_date?: string
  group_id?: number
  timezone?: string
  page?: number
  page_size?: number
  sort?: APIKeyUsageBreakdownSort
}

/** Get the current user's usage ranking grouped by API Key. */
export async function getDashboardAPIKeyBreakdown(
  params?: APIKeyUsageBreakdownParams
): Promise<APIKeyUsageBreakdownResponse> {
  const { data } = await apiClient.get<APIKeyUsageBreakdownResponse>(
    '/usage/dashboard/api-key-breakdown',
    { params }
  )
  return data
}

export type UsageAnalyticsGroupBy = 'model' | 'reasoning_effort' | 'api_key' | 'api_key_model'

export type UsageAnalyticsSort =
  | 'actual_cost_desc'
  | 'cost_desc'
  | 'usage_cost_desc'
  | 'tokens_desc'
  | 'requests_desc'

export interface UsageAnalyticsParams {
  start_date?: string
  end_date?: string
  timezone?: string
  group_by?: UsageAnalyticsGroupBy
  sort?: UsageAnalyticsSort
  /** 1..10000 */
  page?: number
  /** 1..1000 */
  page_size?: number
  api_key_id?: number
  group_id?: number
  /** Exact requested model name. */
  model?: string
  /** Recorded effort value; "none" is a real value, not "missing". */
  reasoning_effort?: string
  /** Records without a recorded effort; mutually exclusive with reasoning_effort. */
  reasoning_effort_missing?: boolean
  request_type?: 'sync' | 'stream' | 'ws_v2' | 'unknown'
  billing_type?: 0 | 1
}

export interface UsageAnalyticsMetrics {
  requests: number
  input_tokens: number
  output_tokens: number
  cache_creation_tokens: number
  cache_read_tokens: number
  total_tokens: number
  /** Standard cost: configured base price before multipliers. */
  cost: number
  /** Billed amount (balance charge or subscription quota usage). */
  actual_cost: number
  /** Reference usage; differs from actual_cost only for hosted groups. */
  usage_cost: number
}

/** Fractions of a row's total tokens; null when the row has no tokens. */
export interface UsageAnalyticsTokenMix {
  input: number | null
  output: number | null
  cache_creation: number | null
  cache_read: number | null
}

/** Fractions of the whole filtered result (all pages); null when the denominator is 0. */
export interface UsageAnalyticsShare {
  requests: number | null
  tokens: number | null
  cost: number | null
  actual_cost: number | null
  usage_cost: number | null
}

export interface UsageAnalyticsItem extends UsageAnalyticsMetrics {
  model?: string
  reasoning_effort?: string
  reasoning_effort_missing?: boolean
  api_key_id?: number
  api_key_name?: string
  api_key_deleted?: boolean
  share: UsageAnalyticsShare
  token_mix: UsageAnalyticsTokenMix
}

export interface UsageAnalyticsSummary extends UsageAnalyticsMetrics {
  token_mix: UsageAnalyticsTokenMix
}

export interface UsageAnalyticsResponse {
  group_by: UsageAnalyticsGroupBy
  sort: UsageAnalyticsSort
  start_date: string
  end_date: string
  timezone: string
  /** RFC3339 range start (inclusive). */
  start: string
  /** RFC3339 range end (exclusive upper bound). */
  end_exclusive: string
  total: number
  page: number
  page_size: number
  pages: number
  summary: UsageAnalyticsSummary
  items: UsageAnalyticsItem[]
}

/** Aggregate the current user's usage by model, reasoning effort, Key or Key × model. */
export async function getUsageAnalytics(
  params: UsageAnalyticsParams,
  options?: { signal?: AbortSignal }
): Promise<UsageAnalyticsResponse> {
  const { data } = await apiClient.get<UsageAnalyticsResponse>('/usage/analytics', {
    params,
    signal: options?.signal
  })
  return data
}

/** Absolute URL of the analytics endpoint, for copyable API examples. */
export function usageAnalyticsEndpointURL(): string {
  const base = String(apiClient.defaults.baseURL || '').replace(/\/+$/, '')
  return new URL(`${base}/usage/analytics`, window.location.origin).href
}

export interface BatchApiKeyUsageStats {
  today_usage_cost?: number
  total_usage_cost?: number
  api_key_id: number
  today_actual_cost: number
  total_actual_cost: number
}

export interface BatchApiKeysUsageResponse {
  stats: Record<string, BatchApiKeyUsageStats>
}

/**
 * Get batch usage stats for user's own API keys
 * @param apiKeyIds - Array of API key IDs
 * @param options - Optional request options
 * @returns Usage stats map keyed by API key ID
 */
export async function getDashboardApiKeysUsage(
  apiKeyIds: number[],
  options?: {
    signal?: AbortSignal
  }
): Promise<BatchApiKeysUsageResponse> {
  const { data } = await apiClient.post<BatchApiKeysUsageResponse>(
    '/usage/dashboard/api-keys-usage',
    {
      api_key_ids: apiKeyIds
    },
    {
      signal: options?.signal
    }
  )
  return data
}

export const usageAPI = {
  list,
  query,
  getStats,
  getStatsByDateRange,
  getByDateRange,
  getById,
  getUsageGroups,
  // Dashboard
  getDashboardStats,
  getDashboardTrend,
  getDashboardModels,
  getDashboardAPIKeyBreakdown,
  getDashboardApiKeysUsage,
  getUsageAnalytics
}

export default usageAPI
