/**
 * Sharing market, owner side. Each owned account is either 自用 (not on the
 * market) or 共享 (sold on a market tier of its 套餐 to other users; my own
 * hosted key then no longer uses it). An administrator assigns the 套餐 once;
 * I switch between the two, at most once per `cooldown_hours`. Sales earn me a
 * share that is frozen for a while, then can be converted into ordinary
 * balance. Nothing here ever names a buyer.
 *
 * Field names follow the server DTOs exactly:
 * - /hosted/owned-accounts/:id/market → service.OwnerMarketView
 * - /hosted/earnings[/entries|/convert] → service.OwnerEarningsView,
 *   OwnerEarningEntriesPage, OwnerEarningConvertView
 * Amounts are exact decimal strings; never parse them into floats for math.
 */
import { api } from './client'
import type { HostedWindow } from './hosted'

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

/** shared ⇔ status "listed". */
export type MarketMode = 'self_use' | 'shared'

export interface OwnerMarketView {
  account_id: number
  market_enabled: boolean
  status: MarketListingStatus | string
  selected_group_id: number | null
  version: number
  /** 套餐 slug set by an administrator (e.g. "max"); null: the account cannot be shared. */
  plan: string | null
  mode: MarketMode | string
  /** At most one switch per this many hours; 0 = no limit. */
  cooldown_hours: number
  /** RFC3339 (database time); null = may switch now. */
  next_switch_at: string | null
  /** Tiers of the account's 套餐. */
  allowed_tiers: MarketTierOption[]
  /** Reason codes why it cannot switch now (see lib/market reasonKey). */
  unavailable_reasons: string[]
  /** May switch to 共享 now. */
  can_list: boolean
  /** May switch back to 自用 now. */
  can_unlist: boolean
  /** Upstream limit windows, same shape as the owned-accounts list (five_hour / seven_day); missing = unknown. */
  upstream_windows: HostedWindow[]
}

/** group_id only when the 套餐 has more than one tier (else the server picks the only one). */
export type OwnerMarketUpdate = { listed: true; group_id?: number } | { listed: false }

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
  const hours = v.cooldown_hours
  return {
    ...v,
    selected_group_id: v.selected_group_id ?? null,
    plan: typeof v.plan === 'string' && v.plan ? v.plan : null,
    mode: v.mode ?? (v.status === 'listed' ? 'shared' : 'self_use'),
    cooldown_hours: typeof hours === 'number' && Number.isFinite(hours) && hours > 0 ? hours : 0,
    next_switch_at: v.next_switch_at ?? null,
    allowed_tiers: v.allowed_tiers ?? [],
    unavailable_reasons: v.unavailable_reasons ?? [],
    can_list: v.can_list === true,
    can_unlist: v.can_unlist === true,
    upstream_windows: Array.isArray(v.upstream_windows) ? v.upstream_windows : []
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
