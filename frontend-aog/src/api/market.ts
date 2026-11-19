/**
 * Sharing market (P3a), owner side. An administrator allows an owned account
 * into some price tiers; the owner picks one and lists / unlists it. Sales to
 * other users earn the owner a share that is frozen for a while, then can be
 * converted into ordinary balance. Nothing here ever names a buyer.
 *
 * Field names follow the server DTOs exactly:
 * - /hosted/owned-accounts/:id/market → service.OwnerMarketView
 * - /hosted/earnings[/entries|/convert] → service.OwnerEarningsView,
 *   OwnerEarningEntriesPage, OwnerEarningConvertView
 * Amounts are exact decimal strings; never parse them into floats for math.
 */
import { api } from './client'

export type MarketListingStatus = 'not_configured' | 'unlisted' | 'listed' | 'revoked'

export interface MarketTierOption {
  group_id: number
  name: string
  rate_multiplier: number
  /** Still a live, active tier. */
  available: boolean
  /** The account is already in this tier through the administrator: it cannot be listed there. */
  admin_member: boolean
}

export interface MarketProtection {
  reserved_concurrency: number
  reserved_devices: number
  reserved_admissions: number
  cap_5h_pct: number
  cap_7d_pct: number
  sample_max_age_minutes: number
}

export interface OwnerMarketView {
  account_id: number
  market_enabled: boolean
  status: MarketListingStatus | string
  selected_group_id: number | null
  version: number
  allowed_tiers: MarketTierOption[]
  protection: MarketProtection | null
  /** Reason codes why it cannot be listed now (see lib/market reasonKey). */
  unavailable_reasons: string[]
  can_list: boolean
}

export type OwnerMarketUpdate = { listed: true; group_id: number } | { listed: false }

export interface OwnerEarnings {
  frozen: string
  available: string
  converted_total: string
  min_convert: string
  as_of: string
}

export type EarningKind = 'accrue' | 'reverse' | 'convert'
export type EarningStatus = 'frozen' | 'available' | 'reversed' | 'converted'

export interface OwnerEarningEntry {
  id: number
  kind: EarningKind | string
  /** Signed: accrue > 0, reverse / convert < 0. */
  amount: string
  status: EarningStatus | string
  available_at: string | null
  created_at: string
  account_id: number | null
  model: string | null
}

export interface OwnerEarningEntriesPage {
  items: OwnerEarningEntry[]
  next_cursor: number | null
}

export interface ConvertResult {
  entry_id: number
  request_id: string
  amount: string
  created_at: string
  /** The same request id was already converted: nothing new happened. */
  replayed: boolean
}

function accountPath(accountId: number): string {
  return `/hosted/owned-accounts/${encodeURIComponent(String(accountId))}/market`
}

function normaliseView(v: OwnerMarketView): OwnerMarketView {
  return {
    ...v,
    selected_group_id: v.selected_group_id ?? null,
    allowed_tiers: v.allowed_tiers ?? [],
    unavailable_reasons: v.unavailable_reasons ?? [],
    protection: v.protection ?? null,
    can_list: v.can_list === true
  }
}

export const marketApi = {
  async getListing(accountId: number): Promise<OwnerMarketView> {
    const { data } = await api.get<OwnerMarketView>(accountPath(accountId))
    return normaliseView(data)
  },
  async setListing(accountId: number, body: OwnerMarketUpdate): Promise<OwnerMarketView> {
    const { data } = await api.put<OwnerMarketView>(accountPath(accountId), body)
    return normaliseView(data)
  },
  async earnings(): Promise<OwnerEarnings> {
    const { data } = await api.get<OwnerEarnings>('/hosted/earnings')
    return data
  },
  async entries(cursor: number | null, limit = 20): Promise<OwnerEarningEntriesPage> {
    const params: Record<string, number> = { limit }
    if (cursor != null) params.cursor = cursor
    const { data } = await api.get<OwnerEarningEntriesPage>('/hosted/earnings/entries', { params })
    return { items: data?.items ?? [], next_cursor: data?.next_cursor ?? null }
  },
  /** `requestId` is the idempotency key: reuse it when retrying the same conversion. */
  async convert(amount: string, requestId: string): Promise<ConvertResult> {
    const { data } = await api.post<ConvertResult>('/hosted/earnings/convert', { amount, request_id: requestId })
    return data
  }
}
