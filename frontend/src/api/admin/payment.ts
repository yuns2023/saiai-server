import { apiClient } from '../client'
import type { PaymentConfig, PaymentOrder, PaymentOrderPage, SubscriptionPlan } from '../payment'

export interface PaymentProvider {
  id: number
  provider_key: string
  name: string
  config: Record<string, string>
  configured_secrets: string[]
  supported_types: string[]
  balance_credit_rate: number
  enabled: boolean
  sort_order: number
  limits: string
}

export interface PaymentProviderInput {
  provider_key: string
  name: string
  config: Record<string, string>
  supported_types: string[]
  balance_credit_rate: number
  enabled: boolean
  sort_order: number
  limits: string
}

export interface PaymentProviderConfigField {
  key: string
  label: string
  kind: 'text' | 'password' | 'url' | 'select'
  required: boolean
  secret: boolean
  placeholder?: string
  options?: Record<string, string>
}

export interface PaymentProviderDefinition {
  key: string
  name: string
  payment_types: string[]
  config_fields: PaymentProviderConfigField[]
  supports_refund: boolean
  webhook_success_body: string
  webhook_failure_body: string
}

export type PaymentConfigUpdate = Partial<PaymentConfig>
export type SubscriptionPlanInput = Omit<SubscriptionPlan, 'id'>

const paymentAdminAPI = {
  async getConfig(): Promise<PaymentConfig> {
    const { data } = await apiClient.get<PaymentConfig>('/admin/payment/config')
    return data
  },
  async updateConfig(input: PaymentConfigUpdate): Promise<PaymentConfig> {
    const { data } = await apiClient.put<PaymentConfig>('/admin/payment/config', input)
    return data
  },
  async listProviders(): Promise<PaymentProvider[]> {
    const { data } = await apiClient.get<PaymentProvider[]>('/admin/payment/providers')
    return data
  },
  async listProviderDefinitions(): Promise<PaymentProviderDefinition[]> {
    const { data } = await apiClient.get<PaymentProviderDefinition[]>('/admin/payment/provider-definitions')
    return data
  },
  async createProvider(input: PaymentProviderInput): Promise<PaymentProvider> {
    const { data } = await apiClient.post<PaymentProvider>('/admin/payment/providers', input)
    return data
  },
  async updateProvider(id: number, input: Partial<PaymentProviderInput>): Promise<PaymentProvider> {
    const { data } = await apiClient.put<PaymentProvider>(`/admin/payment/providers/${id}`, input)
    return data
  },
  async deleteProvider(id: number): Promise<void> {
    await apiClient.delete(`/admin/payment/providers/${id}`)
  },
  async listPlans(): Promise<SubscriptionPlan[]> {
    const { data } = await apiClient.get<SubscriptionPlan[]>('/admin/payment/plans')
    return data
  },
  async createPlan(input: SubscriptionPlanInput): Promise<SubscriptionPlan> {
    const { data } = await apiClient.post<SubscriptionPlan>('/admin/payment/plans', input)
    return data
  },
  async updatePlan(id: number, input: Partial<SubscriptionPlanInput>): Promise<SubscriptionPlan> {
    const { data } = await apiClient.put<SubscriptionPlan>(`/admin/payment/plans/${id}`, input)
    return data
  },
  async deletePlan(id: number): Promise<void> {
    await apiClient.delete(`/admin/payment/plans/${id}`)
  },
  async listOrders(page = 1, pageSize = 20, status = ''): Promise<PaymentOrderPage> {
    const { data } = await apiClient.get<PaymentOrderPage>('/admin/payment/orders', {
      params: { page, page_size: pageSize, status },
    })
    return data
  },
  async retryFulfillment(id: number): Promise<void> {
    await apiClient.post(`/admin/payment/orders/${id}/retry`)
  },
  async requestRefund(id: number, input: { mode: 'automatic' | 'manual'; reason: string; external_reference?: string; force: boolean }): Promise<AdminPaymentOrder> {
    const { data } = await apiClient.post<AdminPaymentOrder>(`/admin/payment/orders/${id}/refund`, input)
    return data
  },
  async resolveRefund(id: number, input: { outcome: 'refunded' | 'not_refunded'; reason: string; external_reference: string }): Promise<AdminPaymentOrder> {
    const { data } = await apiClient.post<AdminPaymentOrder>(`/admin/payment/orders/${id}/refund/resolve`, input)
    return data
  },
}

export type AdminPaymentOrder = PaymentOrder & { user_id: number; user_email: string; user_name: string }
export default paymentAdminAPI
