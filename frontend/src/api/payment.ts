import { apiClient } from './client'

export type PaymentOrderStatus =
  | 'PENDING'
  | 'PAID'
  | 'RECHARGING'
  | 'COMPLETED'
  | 'EXPIRED'
  | 'CANCELLED'
  | 'FAILED'
  | 'REFUND_REQUESTED'
  | 'REFUNDING'
  | 'REFUND_PENDING'
  | 'REFUNDED'
  | 'REFUND_FAILED'

export interface PaymentConfig {
  enabled: boolean
  min_amount: number
  max_amount: number
  order_timeout_minutes: number
  max_pending_orders: number
  recharge_fee_rate: number
}

export interface PaymentOrder {
  id: number
  out_trade_no?: string
  amount: number
  pay_amount: number
  currency: string
  balance_credit_rate: number
  order_type: 'balance' | 'subscription'
  plan_id?: number
  subscription_group_id?: number
  subscription_days?: number
  payment_type?: string
  provider_key?: string
  status: PaymentOrderStatus
  pay_url?: string
  qr_code?: string
  expires_at: string
  paid_at?: string
  completed_at?: string
  failed_reason?: string
  refund_mode?: 'automatic' | 'manual'
  refund_amount?: number
  refund_reason?: string
  refund_external_reference?: string
  refund_requested_by?: string
  refund_requested_at?: string
  refund_provider_call_started_at?: string
  refunded_at?: string
  refund_id?: string
  refund_entitlement_reversed?: boolean
  refund_force?: boolean
  refund_error?: string
  created_at?: string
}

export interface SubscriptionPlan {
  id: number
  group_id: number
  name: string
  description: string
  price: number
  original_price?: number
  currency: string
  validity_days: number
  validity_unit: 'day' | 'month' | 'year'
  features: string
  product_name: string
  for_sale: boolean
  sort_order: number
}

export interface CreatePaymentOrderInput {
  amount?: number
  order_type: 'balance' | 'subscription'
  plan_id?: number
  payment_type: string
  provider_instance_id: number
  is_mobile: boolean
}

export interface PaymentConfigResponse {
  config: PaymentConfig
  payment_methods: PaymentMethod[]
}

export interface PaymentMethod {
  id: string
  provider_instance_id: number
  type: string
  provider_key: string
  currency: string
  balance_credit_rate: number
}

export interface PaymentOrderPage {
  items: PaymentOrder[]
  total: number
  page: number
  page_size: number
  pages: number
}

export const paymentAPI = {
  async getConfig(): Promise<PaymentConfigResponse> {
    const { data } = await apiClient.get<PaymentConfigResponse>('/payment/config')
    return data
  },

  async listPlans(): Promise<SubscriptionPlan[]> {
    const { data } = await apiClient.get<SubscriptionPlan[]>('/payment/plans')
    return data
  },

  async createOrder(input: CreatePaymentOrderInput): Promise<PaymentOrder> {
    const { data } = await apiClient.post<PaymentOrder>('/payment/orders', input)
    return data
  },

  async listMyOrders(page = 1, pageSize = 20): Promise<PaymentOrderPage> {
    const { data } = await apiClient.get<PaymentOrderPage>('/payment/orders/my', {
      params: { page, page_size: pageSize },
    })
    return data
  },

  async getOrder(id: number): Promise<PaymentOrder> {
    const { data } = await apiClient.get<PaymentOrder>(`/payment/orders/${id}`)
    return data
  },

  async cancelOrder(id: number): Promise<void> {
    await apiClient.post(`/payment/orders/${id}/cancel`)
  },
}

export default paymentAPI
