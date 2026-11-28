/**
 * Sharing market, admin side.
 * - /admin/accounts/:id/market-listing: the 套餐 (plan) of an owned account;
 *   its tiers are the live market tiers of that plan, and the owner switches
 *   between 自用 and 共享 (service.AdminMarketListingView).
 * - /admin/market/earnings[/entries|/reverse]: per-owner earnings, the ledger
 *   and the reverse of frozen earnings (service.AdminOwnerEarningsPage …).
 * Field names follow the server DTO json tags; amounts are exact decimal strings.
 */
import { apiClient } from '../client'

export type MarketListingStatus = 'unlisted' | 'listed' | 'revoked'

export interface MarketListing {
  account_id: number
  owner_user_id: number
  /** 套餐 slug; null = none (the owner cannot share). */
  plan: string | null
  /** Derived from the plan (read-only). */
  allowed_group_ids: number[]
  selected_group_id: number | null
  status: MarketListingStatus | string
  version: number
  listed_at: string | null
  created_at: string
  updated_at: string
}

export interface MarketTierOption {
  group_id: number
  name: string
  rate_multiplier: number
  available: boolean
  admin_member: boolean
}

export interface AdminMarketListingView {
  account_id: number
  owner_user_id: number | null
  /** null: never configured. */
  listing: MarketListing | null
  /** Tiers derived from the plan. */
  allowed_tiers: MarketTierOption[]
  /** "" when the account type/config can be shared; else a reason code. */
  ineligible_reason: string
  market_enabled: boolean
}

/** PUT body: the 套餐 slug (required). Creates the listing (自用) or changes its plan; re-grants a revoked one. */
export interface AdminMarketListingUpdate {
  plan: string
}

export interface AdminOwnerEarningSummary {
  owner_user_id: number
  email: string
  frozen: string
  available: string
  converted_total: string
  reversed_total: string
  accrued_total: string
}

export interface AdminOwnerEarningsPage {
  items: AdminOwnerEarningSummary[]
  total: number
  page: number
  page_size: number
  as_of: string
}

export interface AdminOwnerEarningEntry {
  id: number
  kind: 'accrue' | 'reverse' | 'convert' | string
  amount: string
  status: 'frozen' | 'available' | 'reversed' | 'converted' | string
  available_at: string | null
  created_at: string
  account_id: number | null
  model: string | null
  owner_user_id: number
  settlement_event_id: number | null
  reverses_entry_id: number | null
  convert_request_id: string | null
  admin_user_id: number | null
  note: string | null
}

export interface AdminOwnerEarningEntriesPage {
  items: AdminOwnerEarningEntry[]
  next_cursor: number | null
}

/**
 * Server contract: at least one of account_id / owner_user_id (both = that
 * owner's earnings on that account); as_of (required) is the cutoff — only
 * earnings recorded at or before it are reversed (pass the overview's as_of);
 * note is optional for the server (the admin page requires one, a UI rule).
 */
export type ReverseEarningsRequest = (
  | { owner_user_id: number; account_id?: number }
  | { account_id: number; owner_user_id?: number }
) & {
  as_of: string
  note?: string
}

export interface ReverseEarningsResult {
  /** Echo of the request's as_of. */
  cutoff: string
  /** Database time of the frozen check; null when nothing was in scope. */
  as_of: string | null
  reversed_count: number
  reversed_amount: string
  owners: Array<{ owner_user_id: number; count: number; amount: string }>
}

/** Longest note the server keeps (characters). */
export const MARKET_REVERSE_NOTE_MAX = 500

function listingPath(accountId: number): string {
  return `/admin/accounts/${encodeURIComponent(String(accountId))}/market-listing`
}

function normaliseListingView(v: AdminMarketListingView): AdminMarketListingView {
  return {
    ...v,
    listing: v.listing ? { ...v.listing, plan: v.listing.plan || null, allowed_group_ids: v.listing.allowed_group_ids ?? [] } : null,
    allowed_tiers: v.allowed_tiers ?? [],
    ineligible_reason: v.ineligible_reason ?? ''
  }
}

export const marketAPI = {
  async getListing(accountId: number): Promise<AdminMarketListingView> {
    const { data } = await apiClient.get<AdminMarketListingView>(listingPath(accountId))
    return normaliseListingView(data)
  },
  async putListing(accountId: number, body: AdminMarketListingUpdate): Promise<AdminMarketListingView> {
    const { data } = await apiClient.put<AdminMarketListingView>(listingPath(accountId), body)
    return normaliseListingView(data)
  },
  /** Revoke: removes the permission and any market membership (history kept). */
  async revokeListing(accountId: number): Promise<AdminMarketListingView> {
    const { data } = await apiClient.delete<AdminMarketListingView>(listingPath(accountId))
    return normaliseListingView(data)
  },
  async owners(page: number, pageSize: number, ownerUserId?: number): Promise<AdminOwnerEarningsPage> {
    const params: Record<string, number> = { page, page_size: pageSize }
    if (ownerUserId) params.owner_user_id = ownerUserId
    const { data } = await apiClient.get<AdminOwnerEarningsPage>('/admin/market/earnings', { params })
    return { ...data, items: data?.items ?? [] }
  },
  async entries(params: { ownerUserId?: number; cursor?: number | null; limit?: number } = {}): Promise<AdminOwnerEarningEntriesPage> {
    const q: Record<string, number> = {}
    if (params.ownerUserId) q.owner_user_id = params.ownerUserId
    if (params.cursor) q.cursor = params.cursor
    if (params.limit) q.limit = params.limit
    const { data } = await apiClient.get<AdminOwnerEarningEntriesPage>('/admin/market/earnings/entries', { params: q })
    return { items: data?.items ?? [], next_cursor: data?.next_cursor ?? null }
  },
  async reverse(req: ReverseEarningsRequest): Promise<ReverseEarningsResult> {
    const body: Record<string, unknown> = { as_of: req.as_of }
    if (req.note !== undefined) body.note = req.note
    if (req.account_id) body.account_id = req.account_id
    if (req.owner_user_id) body.owner_user_id = req.owner_user_id
    const { data } = await apiClient.post<ReverseEarningsResult>('/admin/market/earnings/reverse', body)
    return { ...data, owners: data?.owners ?? [] }
  }
}

export default marketAPI
