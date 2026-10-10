import type { LongContextPricing } from '@/types'
import { apiClient } from '../client'

export const priceFields = ['input', 'output', 'cache_read', 'cache_write_5m', 'cache_write_1h', 'priority_input', 'priority_output', 'priority_cache_read', 'priority_cache_write'] as const
export type PriceField = typeof priceFields[number]
export type PriceOverride = Partial<Record<PriceField, number>>
export interface UnitPrices extends Record<string, number | boolean | Record<string, boolean> | undefined> {
  input: number
  output: number
  cache_read: number
  cache_write_5m: number
  cache_write_1h: number
  long_context_threshold: number
}
export interface PricePair { reference: Record<string, number>; effective: Record<string, number> }
export interface ModelPrice {
  model: string
  alias?: string
  resolved_model: string
  source: string
  version: string
  recent: boolean
  mode: string
  available: boolean
  reference: UnitPrices | null
  effective: UnitPrices | null
  override: PriceOverride
  tiers: Record<string, PricePair>
  long_context_tiers?: Record<string, PricePair>
}
export interface PricingMetadata { source: string; source_url: string; updated_at: string; source_hash: string; model_count: number }
export interface PriceList { items: ModelPrice[]; total: number; page: number; page_size: number; metadata: PricingMetadata }
export interface PriceEdit { at: string; actor_id: number; model: string; before_alias: string; after_alias: string; before: PriceOverride; after: PriceOverride }
export interface PriceEditInput { model: string; alias: string; override: PriceOverride; expected_version: string }
export interface PreviewInput {
  model: string
  alias: string
  override: PriceOverride
  tokens: Record<string, number>
  service_tier: string
  group_rate: number
  model_rate: number
  user_discount: number
  subscription: boolean
}
export interface PricePreview {
  unit_prices: PricePair
  long_context_unit_prices?: PricePair
  charged_amount: number
  subscription: boolean
  cost: {
    input_cost: number
    output_cost: number
    cache_creation_5m_cost: number
    cache_creation_1h_cost: number
    cache_read_cost: number
    total_cost: number
    actual_cost: number
    pricing_snapshot: { long_context?: LongContextPricing; version: string; reference_total_cost: number; reference: UnitPrices; effective: UnitPrices; resolved_model: string }
  }
}
export async function list(params: { search: string; configured: boolean; page: number }): Promise<PriceList> {
  const { data } = await apiClient.get<PriceList>('/admin/settings/model-pricing', { params })
  return data
}
export async function update(input: PriceEditInput): Promise<ModelPrice> {
  const { data } = await apiClient.put<ModelPrice>('/admin/settings/model-pricing', input)
  return data
}
export async function preview(input: PreviewInput): Promise<PricePreview> {
  const { data } = await apiClient.post<PricePreview>('/admin/settings/model-pricing/preview', input)
  return data
}
export async function history(): Promise<PriceEdit[]> {
  const { data } = await apiClient.get<PriceEdit[]>('/admin/settings/model-pricing/history')
  return data
}
export default { list, update, preview, history }
