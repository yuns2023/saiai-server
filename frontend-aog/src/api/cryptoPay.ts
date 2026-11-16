/**
 * Stablecoin top-up (/api/v1/crypto-pay). Every amount is a decimal string;
 * compare and format with '@/lib/decimal', never Number arithmetic.
 */
import { api } from './client'

export interface CryptoPayAsset {
  symbol: string
  contract: string
}

export interface CryptoPayNetwork {
  network: string
  label: string
  assets: CryptoPayAsset[]
  finality_hint_minutes: number
  /** Template with "{tx}". */
  explorer_tx_url: string
  /** Template with "{addr}". */
  explorer_address_url: string
}

export interface CryptoPayConfig {
  enabled: boolean
  min_amount: string
  max_amount: string
  order_ttl_minutes: number
  networks: CryptoPayNetwork[]
}

export type CryptoDepositStatus = 'confirming' | 'credited' | 'held' | 'orphaned'

export interface CryptoDeposit {
  id: number
  network: string
  asset: string
  amount: string
  tx_hash: string
  status: CryptoDepositStatus
  block_time: string
  credited_at: string | null
}

export type CryptoOrderStatus = 'pending' | 'paid' | 'expired'

export interface CryptoOrder {
  id: number
  network: string
  asset: string
  amount: string
  address: string
  status: CryptoOrderStatus
  created_at: string
  expires_at: string
  /** Finalized inflows only (incl. credited/held) since created_at, all assets; no overlap with confirming_amount. */
  received_amount: string
  /** Observed, not yet finalized. */
  confirming_amount: string
  credited_amount: string
  /** Newest first. */
  deposits: CryptoDeposit[]
}

export interface CryptoPage<T> {
  items: T[]
  total: number
  page: number
  page_size: number
}

export interface CryptoAddress {
  network: string
  address: string
}

export interface CreateCryptoOrderInput {
  network: string
  asset: string
  amount: string
}

/** Structured reasons the order endpoint can fail with. */
export const CRYPTO_PAY_ADDRESS_UNAVAILABLE = 'CRYPTO_PAY_ADDRESS_UNAVAILABLE'
export const CRYPTO_PAY_DISABLED = 'CRYPTO_PAY_DISABLED'
export const CRYPTO_PAY_AMOUNT_INVALID = 'CRYPTO_PAY_AMOUNT_INVALID'
export const CRYPTO_PAY_NETWORK_UNSUPPORTED = 'CRYPTO_PAY_NETWORK_UNSUPPORTED'

export const cryptoPayApi = {
  async config(): Promise<CryptoPayConfig> {
    const { data } = await api.get<CryptoPayConfig>('/crypto-pay/config')
    return { ...data, networks: data?.networks ?? [] }
  },
  async createOrder(input: CreateCryptoOrderInput): Promise<CryptoOrder> {
    const { data } = await api.post<CryptoOrder>('/crypto-pay/orders', input)
    return data
  },
  async getOrder(id: number | string): Promise<CryptoOrder> {
    const { data } = await api.get<CryptoOrder>(`/crypto-pay/orders/${encodeURIComponent(String(id))}`)
    return { ...data, deposits: data?.deposits ?? [] }
  },
  async myOrders(page = 1, pageSize = 20): Promise<CryptoPage<CryptoOrder>> {
    const { data } = await api.get<CryptoPage<CryptoOrder>>('/crypto-pay/orders/my', { params: { page, page_size: pageSize } })
    return { ...data, items: data?.items ?? [] }
  },
  async deposits(page = 1, pageSize = 20): Promise<CryptoPage<CryptoDeposit>> {
    const { data } = await api.get<CryptoPage<CryptoDeposit>>('/crypto-pay/deposits', { params: { page, page_size: pageSize } })
    return { ...data, items: data?.items ?? [] }
  },
  async addresses(): Promise<CryptoAddress[]> {
    const { data } = await api.get<CryptoAddress[]>('/crypto-pay/addresses')
    return data ?? []
  }
}

/** Fills an explorer template ("…/tx/{tx}"); only https links are returned, otherwise ''. */
export function explorerUrl(template: string | undefined, key: 'tx' | 'addr', value: string): string {
  if (!template || !value) return ''
  const url = template.split(`{${key}}`).join(encodeURIComponent(value))
  try {
    return new URL(url).protocol === 'https:' ? url : ''
  } catch {
    return ''
  }
}

/** Total pages for a {total, page_size} list. */
export function pageCount(p: { total: number; page_size: number }): number {
  return p.page_size > 0 ? Math.max(1, Math.ceil(p.total / p.page_size)) : 1
}

/** Maps an order-creation failure to a known reason (by structured reason/code, then HTTP status). */
export function cryptoOrderError(err: unknown): 'addressUnavailable' | 'disabled' | 'amountInvalid' | 'networkUnsupported' | null {
  const e = (err ?? {}) as { status?: number; code?: unknown; reason?: unknown }
  const tag = typeof e.reason === 'string' ? e.reason : typeof e.code === 'string' ? e.code : ''
  if (tag === CRYPTO_PAY_ADDRESS_UNAVAILABLE) return 'addressUnavailable'
  if (tag === CRYPTO_PAY_DISABLED) return 'disabled'
  if (tag === CRYPTO_PAY_AMOUNT_INVALID) return 'amountInvalid'
  if (tag === CRYPTO_PAY_NETWORK_UNSUPPORTED) return 'networkUnsupported'
  if (!tag && e.status === 409) return 'addressUnavailable'
  if (!tag && e.status === 503) return 'disabled'
  return null
}
