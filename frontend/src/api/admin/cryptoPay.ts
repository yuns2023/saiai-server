/**
 * Admin stablecoin top-up (/api/v1/admin/crypto-pay).
 * Amounts are decimal strings from the server; display them as-is, never Number arithmetic.
 */
import { apiClient } from '../client'
import type { PaginatedResponse } from '@/types'

export type CryptoDepositRawStatus = 'observed' | 'finalized' | 'credited' | 'held' | 'orphaned'

/** "held" = still held; "released" = held, then credited by an admin; "any" = both. */
export type CryptoHoldFilter = 'held' | 'released' | 'any'

export type CryptoHoldReason = 'amount_over_threshold' | 'user_inactive' | 'asset_not_whitelisted'

export interface CryptoAdminDeposit {
  id: number
  user_id: number
  user_email: string
  user_name: string
  user_status: string
  user_deleted: boolean
  network: string
  asset: string
  asset_contract: string
  address: string
  token_account: string
  from_address: string
  units: string
  amount: string
  tx_hash: string
  event_key: string
  block_number: number
  block_hash: string
  block_time: string
  status: CryptoDepositRawStatus
  hold_reason: CryptoHoldReason | ''
  observed_at: string
  finalized_at: string | null
  credited_at: string | null
  orphaned_at: string | null
  /** Ledger operator ("admin:<id>" for a release, "system" for auto credit); "" while not credited. */
  credited_by: string
  /** Template with "{tx}"; "" when the network is not configured here. */
  explorer_tx_url: string
  /** Template with "{addr}". */
  explorer_address_url: string
}

export interface CryptoCreditOutcome {
  deposit_id: number
  status: CryptoDepositRawStatus
  hold_reason: string
  /** True only when this call moved money; false = it was already credited. */
  credited: boolean
}

export interface CryptoAdminSettings {
  feature_configured: boolean
  hold_threshold_amount: string
  auto_credit_enabled: boolean
  invalid_settings: string[]
  networks: string[]
}

export interface ListCryptoDepositsParams {
  hold?: CryptoHoldFilter
  status?: string
  network?: string
  user_id?: number
}

const cryptoPayAdminAPI = {
  async getSettings(): Promise<CryptoAdminSettings> {
    const { data } = await apiClient.get<CryptoAdminSettings>('/admin/crypto-pay/settings')
    return data
  },
  async listDeposits(page: number, pageSize: number, params: ListCryptoDepositsParams = {}): Promise<PaginatedResponse<CryptoAdminDeposit>> {
    const { data } = await apiClient.get<PaginatedResponse<CryptoAdminDeposit>>('/admin/crypto-pay/deposits', {
      params: { page, page_size: pageSize, ...params }
    })
    return { ...data, items: data?.items ?? [] }
  },
  async releaseDeposit(id: number): Promise<CryptoCreditOutcome> {
    const { data } = await apiClient.post<CryptoCreditOutcome>(`/admin/crypto-pay/deposits/${id}/release`)
    return data
  }
}

/** Fills an explorer template ("…/tx/{tx}"); only https links are returned, otherwise ''. */
export function cryptoExplorerUrl(template: string | undefined, key: 'tx' | 'addr', value: string): string {
  if (!template || !value) return ''
  const url = template.split(`{${key}}`).join(encodeURIComponent(value))
  try {
    return new URL(url).protocol === 'https:' ? url : ''
  } catch {
    return ''
  }
}

export default cryptoPayAdminAPI
