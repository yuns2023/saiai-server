/**
 * Buying balance redeem codes (/api/v1/redeem/purchases). The buyer is always
 * the signed-in user; codes are redeemed through the normal /redeem flow.
 */
import { api } from './client'

export type RedeemPurchaseStatus = 'unused' | 'used' | 'revoked' | 'unknown'

export interface RedeemPurchase {
  id: number
  code: string
  amount: number
  status: RedeemPurchaseStatus
  used_at: string | null
  created_at: string
  revoked_at: string | null
}

/** Remaining values are a display snapshot; the server re-checks on purchase. */
export interface RedeemPurchaseConfig {
  enabled: boolean
  min_amount: number
  max_amount: number
  remaining_amount_24h: number
  remaining_count_24h: number
}

export interface RedeemPurchaseOverview {
  config: RedeemPurchaseConfig
  items: RedeemPurchase[]
  pagination: { total: number; page: number; page_size: number; pages: number }
}

/** `replayed`: the request_id was already used with this amount; this is the original purchase. */
export interface RedeemPurchaseResult extends RedeemPurchase {
  replayed: boolean
}

export const redeemPurchaseApi = {
  async overview(page: number, pageSize: number): Promise<RedeemPurchaseOverview> {
    const { data } = await api.get<RedeemPurchaseOverview>('/redeem/purchases', { params: { page, page_size: pageSize } })
    return data
  },
  /** `amount` is the normalised decimal string ("10.5"); `requestId` makes retries safe. */
  async purchase(amount: string, requestId: string): Promise<RedeemPurchaseResult> {
    const { data } = await api.post<RedeemPurchaseResult>('/redeem/purchases', { amount, request_id: requestId })
    return data
  },
  async revoke(id: number): Promise<RedeemPurchase> {
    const { data } = await api.post<RedeemPurchase>(`/redeem/purchases/${id}/revoke`)
    return data
  }
}

/** A v4 UUID; crypto.randomUUID only exists in secure contexts. */
export function newRequestId(): string {
  const c = globalThis.crypto
  if (typeof c?.randomUUID === 'function') return c.randomUUID()
  const b = c.getRandomValues(new Uint8Array(16))
  b[6] = (b[6] & 0x0f) | 0x40
  b[8] = (b[8] & 0x3f) | 0x80
  const h = Array.from(b, (x) => x.toString(16).padStart(2, '0')).join('')
  return `${h.slice(0, 8)}-${h.slice(8, 12)}-${h.slice(12, 16)}-${h.slice(16, 20)}-${h.slice(20)}`
}
