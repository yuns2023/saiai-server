<template>
  <AppLayout>
    <div class="mx-auto max-w-5xl space-y-6">
      <div>
        <h1 class="text-2xl font-semibold text-gray-900 dark:text-white">{{ text.title }}</h1>
        <p class="mt-1 text-sm text-gray-500 dark:text-dark-400">{{ text.description }}</p>
      </div>

      <div v-if="loading" class="card flex min-h-56 items-center justify-center">
        <div class="h-8 w-8 animate-spin rounded-full border-2 border-primary-500 border-t-transparent"></div>
      </div>

      <div v-else-if="!config?.enabled" class="card py-14 text-center">
        <Icon name="creditCard" size="lg" class="mx-auto text-gray-400" />
        <h2 class="mt-4 text-lg font-medium text-gray-900 dark:text-white">{{ text.disabled }}</h2>
        <p class="mt-2 text-sm text-gray-500 dark:text-dark-400">{{ text.disabledHint }}</p>
      </div>

      <template v-else>
        <div class="grid gap-6 lg:grid-cols-[1fr_1fr]">
          <form class="card space-y-5" @submit.prevent="createOrder">
            <div class="grid grid-cols-2 gap-2 rounded-xl bg-gray-100 p-1 dark:bg-dark-800">
              <button type="button" class="rounded-lg px-3 py-2 text-sm font-medium" :class="purchaseMode === 'balance' ? 'bg-white text-primary-700 shadow dark:bg-dark-700 dark:text-primary-300' : 'text-gray-500'" @click="purchaseMode = 'balance'">{{ text.balance }}</button>
              <button type="button" class="rounded-lg px-3 py-2 text-sm font-medium" :class="purchaseMode === 'subscription' ? 'bg-white text-primary-700 shadow dark:bg-dark-700 dark:text-primary-300' : 'text-gray-500'" :disabled="plans.length === 0" @click="purchaseMode = 'subscription'">{{ text.subscription }}</button>
            </div>

            <div v-if="purchaseMode === 'subscription'">
              <label class="label">{{ text.plan }}</label>
              <div class="mt-2 space-y-3">
                <button
                  v-for="plan in plans"
                  :key="plan.id"
                  type="button"
                  class="w-full rounded-xl border p-4 text-left transition"
                  :class="selectedPlanID === plan.id ? 'border-primary-500 bg-primary-50 dark:bg-primary-900/20' : 'border-gray-200 dark:border-dark-600'"
                  @click="selectedPlanID = plan.id"
                >
                  <div class="flex items-start justify-between gap-3">
                    <div><p class="font-medium text-gray-900 dark:text-white">{{ plan.name }}</p><p class="mt-1 text-xs text-gray-500">{{ validityText(plan) }}</p></div>
                    <div class="text-right"><p class="font-semibold text-gray-900 dark:text-white">{{ formatMoney(plan.price, plan.currency) }}</p><p v-if="plan.original_price" class="text-xs text-gray-400 line-through">{{ formatMoney(plan.original_price, plan.currency) }}</p></div>
                  </div>
                  <p v-if="plan.description" class="mt-2 text-sm text-gray-500">{{ plan.description }}</p>
                  <p v-if="plan.features" class="mt-2 whitespace-pre-line text-xs text-gray-500">{{ plan.features }}</p>
                </button>
              </div>
            </div>

            <div v-else>
              <label class="label">{{ text.amount }}</label>
              <div class="relative mt-2">
                <span class="absolute left-3 top-1/2 -translate-y-1/2 text-gray-500">$</span>
                <input
                  v-model.number="amount"
                  class="input w-full pl-8"
                  type="number"
                  step="0.01"
                  :min="config.min_amount"
                  :max="config.max_amount"
                  required
                />
              </div>
              <div class="mt-2 flex flex-wrap gap-2">
                <button v-for="value in quickAmounts" :key="value" type="button" class="btn btn-secondary btn-sm" @click="amount = value">
                  ${{ value }}
                </button>
              </div>
              <p class="mt-2 text-xs text-gray-500">${{ config.min_amount.toFixed(2) }} – ${{ config.max_amount.toFixed(2) }} USD {{ text.credit }}</p>
            </div>

            <div>
              <label class="label">{{ text.method }}</label>
              <div class="mt-2 grid grid-cols-2 gap-3">
                <button
                  v-for="method in availablePaymentMethods"
                  :key="method.id"
                  type="button"
                  class="rounded-xl border px-4 py-3 text-sm font-medium transition"
                  :class="paymentMethodID === method.id ? 'border-primary-500 bg-primary-50 text-primary-700 dark:bg-primary-900/20 dark:text-primary-300' : 'border-gray-200 text-gray-700 hover:border-gray-300 dark:border-dark-600 dark:text-dark-200'"
                  @click="paymentMethodID = method.id"
                >
                  {{ paymentMethodLabel(method.type) }} · {{ method.currency }}
                </button>
              </div>
            </div>

            <div v-if="purchaseMode === 'balance' && config.recharge_fee_rate > 0" class="rounded-lg bg-gray-50 p-3 text-sm dark:bg-dark-800">
              {{ text.payAmount }}：{{ calculatedPayAmount }}
              <span class="ml-1 text-gray-500">({{ text.fee }} {{ config.recharge_fee_rate }}%)</span>
            </div>

            <button class="btn btn-primary w-full" type="submit" :disabled="submitting || availablePaymentMethods.length === 0 || (purchaseMode === 'subscription' && !selectedPlanID)">
              {{ submitting ? text.creating : text.pay }}
            </button>
          </form>

          <div class="card min-h-80">
            <div v-if="!activeOrder" class="flex h-full min-h-72 flex-col items-center justify-center text-center text-gray-500">
              <Icon name="creditCard" size="lg" class="mb-3 text-gray-300" />
              <p>{{ text.orderHint }}</p>
            </div>
            <div v-else class="space-y-4 text-center">
              <div class="flex items-center justify-between text-left">
                <div>
                  <p class="text-sm text-gray-500">{{ text.order }} #{{ activeOrder.id }}</p>
                  <p class="mt-1 text-2xl font-semibold text-gray-900 dark:text-white">{{ formatMoney(activeOrder.pay_amount, activeOrder.currency) }}</p>
                </div>
                <span class="rounded-full px-3 py-1 text-xs font-medium" :class="statusClass(activeOrder.status)">{{ statusText(activeOrder.status) }}</span>
              </div>

              <img v-if="qrCodeImage && activeOrder.status === 'PENDING'" :src="qrCodeImage" class="mx-auto h-52 w-52 rounded-lg bg-white p-2" alt="Payment QR code" />
              <p v-if="activeOrder.status === 'PENDING'" class="text-sm text-gray-500">{{ text.scanHint }}</p>
              <a v-if="safePayURL && activeOrder.status === 'PENDING'" :href="safePayURL" target="_blank" rel="noopener noreferrer" class="btn btn-primary inline-flex">
                {{ text.openPayment }}
              </a>
              <button v-if="activeOrder.status === 'PENDING'" class="btn btn-secondary ml-2" type="button" @click="cancelOrder">{{ text.cancel }}</button>
              <p v-if="activeOrder.status === 'COMPLETED'" class="rounded-lg bg-green-50 p-4 text-sm text-green-700 dark:bg-green-900/20 dark:text-green-300">{{ activeOrder.order_type === 'subscription' ? text.subscriptionCompleted : text.completed }}</p>
              <p v-else-if="activeOrder.status === 'FAILED'" class="rounded-lg bg-red-50 p-4 text-sm text-red-700 dark:bg-red-900/20 dark:text-red-300">{{ activeOrder.failed_reason || text.failed }}</p>
            </div>
          </div>
        </div>

        <div class="card">
          <h2 class="text-lg font-medium text-gray-900 dark:text-white">{{ text.history }}</h2>
          <div v-if="orders.length === 0" class="py-10 text-center text-sm text-gray-500">{{ text.noOrders }}</div>
          <div v-else class="mt-4 divide-y divide-gray-100 dark:divide-dark-700">
            <div v-for="order in orders" :key="order.id" class="flex items-center justify-between gap-4 py-3">
              <div>
                <p class="font-medium text-gray-900 dark:text-white">{{ order.order_type === 'subscription' ? text.subscription : text.balance }} · {{ formatMoney(order.amount, order.order_type === 'balance' ? 'USD' : order.currency) }}</p>
                <p class="mt-1 text-xs text-gray-500">#{{ order.id }} · {{ formatDate(order.created_at || order.expires_at) }}</p>
              </div>
              <span class="rounded-full px-3 py-1 text-xs font-medium" :class="statusClass(order.status)">{{ statusText(order.status) }}</span>
            </div>
          </div>
        </div>
      </template>
    </div>
  </AppLayout>
</template>

<script setup lang="ts">
import { computed, onMounted, onUnmounted, ref, watch } from 'vue'
import { useI18n } from 'vue-i18n'
import QRCode from 'qrcode'
import AppLayout from '@/components/layout/AppLayout.vue'
import Icon from '@/components/icons/Icon.vue'
import { useAppStore } from '@/stores'
import { useAuthStore } from '@/stores/auth'
import { paymentAPI, type PaymentConfig, type PaymentMethod, type PaymentOrder, type PaymentOrderStatus, type SubscriptionPlan } from '@/api/payment'

const { locale } = useI18n()
const appStore = useAppStore()
const authStore = useAuthStore()
const loading = ref(true)
const submitting = ref(false)
const config = ref<PaymentConfig | null>(null)
const paymentMethods = ref<PaymentMethod[]>([])
const paymentMethodID = ref('')
const purchaseMode = ref<'balance' | 'subscription'>('balance')
const plans = ref<SubscriptionPlan[]>([])
const selectedPlanID = ref<number | null>(null)
const amount = ref(50)
const activeOrder = ref<PaymentOrder | null>(null)
const orders = ref<PaymentOrder[]>([])
const qrCodeImage = ref('')
let pollTimer: ReturnType<typeof setInterval> | null = null

const text = computed(() => locale.value.startsWith('zh') ? {
  title: '购买与充值', description: '通过 SAIAI 原生支付购买订阅或充值余额，到账过程自动完成。', disabled: '原生支付暂未开启',
  disabledHint: '请联系管理员配置支付渠道并启用支付。', amount: '按量额度（USD）', method: '支付方式', alipay: '支付宝', wechat: '微信支付',
  balance: '按量额度', subscription: '订阅套餐', plan: '选择套餐', credit: '额度',
  payAmount: '实际支付', fee: '手续费', creating: '正在创建订单…', pay: '立即支付', orderHint: '创建订单后，支付信息会显示在这里。',
  order: '订单', scanHint: '请扫码或打开支付页面完成付款', openPayment: '打开支付页面', cancel: '取消订单', completed: '充值已到账，账户余额已更新。', subscriptionCompleted: '订阅已生效或完成续期。',
  failed: '充值入账失败，请联系管理员处理。', history: '充值记录', noOrders: '暂无充值订单',
} : {
  title: 'Purchase and recharge', description: 'Buy a subscription or recharge securely through SAIAI native payments.', disabled: 'Native payments are disabled',
  disabledHint: 'Ask an administrator to configure and enable a payment provider.', amount: 'Usage credit (USD)', method: 'Payment method', alipay: 'Alipay', wechat: 'WeChat Pay',
  balance: 'Usage credit', subscription: 'Subscription', plan: 'Choose a plan', credit: 'credit',
  payAmount: 'Total payment', fee: 'fee', creating: 'Creating order…', pay: 'Pay now', orderHint: 'Payment details will appear here after you create an order.',
  order: 'Order', scanHint: 'Scan the code or open the payment page to complete payment', openPayment: 'Open payment page', cancel: 'Cancel', completed: 'Recharge completed and your balance has been updated.', subscriptionCompleted: 'Your subscription is now active or has been extended.',
  failed: 'Fulfillment failed. Please contact an administrator.', history: 'Recharge history', noOrders: 'No recharge orders yet',
})

const quickAmounts = computed(() => [10, 50, 100, 200].filter(value => config.value && value >= config.value.min_amount && value <= config.value.max_amount))
const selectedPlan = computed(() => plans.value.find(plan => plan.id === selectedPlanID.value))
const availablePaymentMethods = computed(() => paymentMethods.value.filter(method => purchaseMode.value === 'balance' || method.currency === selectedPlan.value?.currency))
const selectedPaymentMethod = computed(() => availablePaymentMethods.value.find(method => method.id === paymentMethodID.value))
const calculatedPayAmount = computed(() => {
  const method = selectedPaymentMethod.value
  if (!method) return '—'
  if (purchaseMode.value === 'subscription' && selectedPlan.value) return formatMoney(selectedPlan.value.price, method.currency)
  const settlement = (Number(amount.value) || 0) / method.balance_credit_rate
  return formatMoney(settlement * (1 + (config.value?.recharge_fee_rate || 0) / 100), method.currency)
})
const safePayURL = computed(() => {
  const raw = activeOrder.value?.pay_url?.trim()
  if (!raw) return ''
  try {
    const parsed = new URL(raw)
    return parsed.protocol === 'https:' || parsed.protocol === 'http:' ? parsed.toString() : ''
  } catch { return '' }
})

function statusText(status: PaymentOrderStatus): string {
  const zh: Record<PaymentOrderStatus, string> = { PENDING: '待支付', PAID: '已支付', RECHARGING: '入账中', COMPLETED: '已完成', EXPIRED: '已过期', CANCELLED: '已取消', FAILED: '入账失败', REFUND_REQUESTED: '已申请退款', REFUNDING: '退款处理中', REFUND_PENDING: '退款待确认', REFUNDED: '已退款', REFUND_FAILED: '退款未完成' }
  const en: Record<PaymentOrderStatus, string> = { PENDING: 'Pending', PAID: 'Paid', RECHARGING: 'Fulfilling', COMPLETED: 'Completed', EXPIRED: 'Expired', CANCELLED: 'Cancelled', FAILED: 'Failed', REFUND_REQUESTED: 'Refund requested', REFUNDING: 'Refunding', REFUND_PENDING: 'Refund pending review', REFUNDED: 'Refunded', REFUND_FAILED: 'Refund not completed' }
  return (locale.value.startsWith('zh') ? zh : en)[status] || status
}

function statusClass(status: PaymentOrderStatus): string {
  if (status === 'COMPLETED' || status === 'REFUNDED') return 'bg-green-100 text-green-700 dark:bg-green-900/30 dark:text-green-300'
  if (status === 'FAILED' || status === 'EXPIRED' || status === 'CANCELLED' || status === 'REFUND_FAILED') return 'bg-gray-100 text-gray-600 dark:bg-dark-700 dark:text-dark-300'
  return 'bg-amber-100 text-amber-700 dark:bg-amber-900/30 dark:text-amber-300'
}

function paymentMethodLabel(method: string): string {
  if (method === 'alipay') return text.value.alipay
  if (method === 'wxpay') return text.value.wechat
  return method
}

async function loadOrders(): Promise<void> {
  const page = await paymentAPI.listMyOrders(1, 20)
  orders.value = page.items || []
}

async function renderQRCode(): Promise<void> {
  const value = activeOrder.value?.qr_code || safePayURL.value
  qrCodeImage.value = value ? await QRCode.toDataURL(value, { width: 320, margin: 1 }) : ''
}

async function createOrder(): Promise<void> {
  if (!config.value || !selectedPaymentMethod.value) return

  submitting.value = true
  try {
    activeOrder.value = await paymentAPI.createOrder({
      amount: purchaseMode.value === 'balance' ? Number(amount.value) : undefined,
      order_type: purchaseMode.value,
      plan_id: purchaseMode.value === 'subscription' ? selectedPlanID.value || undefined : undefined,
      payment_type: selectedPaymentMethod.value.type,
      provider_instance_id: selectedPaymentMethod.value.provider_instance_id,
      is_mobile: /mobile/i.test(navigator.userAgent),
    })
    await renderQRCode()
    await loadOrders()
    startPolling()
  } catch (error: any) {
    appStore.showError(error?.message || 'Failed to create payment order')
  } finally {
    submitting.value = false
  }
}

function validityText(plan: SubscriptionPlan): string {
  const units = locale.value.startsWith('zh')
    ? { day: '天', month: '个月', year: '年' }
    : { day: plan.validity_days === 1 ? 'day' : 'days', month: plan.validity_days === 1 ? 'month' : 'months', year: plan.validity_days === 1 ? 'year' : 'years' }
  return locale.value.startsWith('zh') ? `${plan.validity_days}${units[plan.validity_unit]}` : `${plan.validity_days} ${units[plan.validity_unit]}`
}

async function pollOrder(): Promise<void> {
  if (!activeOrder.value) return
  try {
    const latest = await paymentAPI.getOrder(activeOrder.value.id)
    const wasCompleted = activeOrder.value.status === 'COMPLETED'
    activeOrder.value = latest
    if (latest.status === 'COMPLETED' && !wasCompleted) {
      stopPolling()
      await Promise.all([loadOrders(), authStore.refreshUser()])
      appStore.showSuccess(text.value.completed)
    } else if (!['PENDING', 'PAID', 'RECHARGING'].includes(latest.status)) {
      stopPolling()
      await loadOrders()
    }
  } catch { /* retry on the next polling interval */ }
}

function startPolling(): void {
  stopPolling()
  pollTimer = setInterval(pollOrder, 3000)
}
function stopPolling(): void {
  if (pollTimer) clearInterval(pollTimer)
  pollTimer = null
}

async function cancelOrder(): Promise<void> {
  if (!activeOrder.value) return
  try {
    await paymentAPI.cancelOrder(activeOrder.value.id)
    await pollOrder()
  } catch (error: any) {
    appStore.showError(error?.message || 'Failed to cancel payment order')
  }
}

function formatDate(value: string): string {
  return new Intl.DateTimeFormat(locale.value, { dateStyle: 'short', timeStyle: 'short' }).format(new Date(value))
}

function formatMoney(value: number, currency = 'CNY'): string {
  try { return new Intl.NumberFormat(locale.value, { style: 'currency', currency }).format(value) }
  catch { return `${currency} ${value.toFixed(2)}` }
}

onMounted(async () => {
  try {
    const result = await paymentAPI.getConfig()
    config.value = result.config
    paymentMethods.value = result.payment_methods || []
    paymentMethodID.value = availablePaymentMethods.value[0]?.id || ''
    amount.value = Math.min(Math.max(50, result.config.min_amount), result.config.max_amount)
    if (result.config.enabled) {
      const [loadedPlans] = await Promise.all([paymentAPI.listPlans(), loadOrders()])
      plans.value = loadedPlans || []
      selectedPlanID.value = plans.value[0]?.id || null
    }
  } catch (error: any) {
    appStore.showError(error?.message || 'Failed to load payment configuration')
  } finally {
    loading.value = false
  }
})

watch(availablePaymentMethods, methods => {
  if (!methods.some(method => method.id === paymentMethodID.value)) paymentMethodID.value = methods[0]?.id || ''
})

onUnmounted(stopPolling)
</script>
