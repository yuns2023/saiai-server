<template>
  <AppLayout>
    <div class="space-y-6">
      <div>
        <h1 class="text-2xl font-semibold text-gray-900 dark:text-white">原生支付</h1>
        <p class="mt-1 text-sm text-gray-500 dark:text-dark-400">管理 SAIAI 内置余额充值、EasyPay 渠道和入账订单。</p>
      </div>

      <div v-if="loading" class="card py-16 text-center text-gray-500">正在加载支付配置…</div>
      <template v-else>
        <form class="card space-y-5" @submit.prevent="saveConfig">
          <div class="flex items-center justify-between gap-4">
            <div>
              <h2 class="text-lg font-medium text-gray-900 dark:text-white">支付开关与限制</h2>
              <p class="mt-1 text-sm text-gray-500">默认关闭；至少启用一个渠道后才能开启。</p>
            </div>
            <label class="flex items-center gap-2 text-sm">
              <input v-model="config.enabled" type="checkbox" class="h-4 w-4" />
              启用原生支付
            </label>
          </div>
          <div class="grid gap-4 sm:grid-cols-2 lg:grid-cols-5">
            <label class="text-sm">最低按量额度（USD）<input v-model.number="config.min_amount" class="input mt-1 w-full" type="number" min="0.01" step="0.01" /></label>
            <label class="text-sm">最高按量额度（USD）<input v-model.number="config.max_amount" class="input mt-1 w-full" type="number" min="0.01" step="0.01" /></label>
            <label class="text-sm">订单超时（分钟）<input v-model.number="config.order_timeout_minutes" class="input mt-1 w-full" type="number" min="1" /></label>
            <label class="text-sm">每用户待支付上限<input v-model.number="config.max_pending_orders" class="input mt-1 w-full" type="number" min="1" /></label>
            <label class="text-sm">手续费率（%）<input v-model.number="config.recharge_fee_rate" class="input mt-1 w-full" type="number" min="0" max="100" step="0.01" /></label>
          </div>
          <button class="btn btn-primary" type="submit" :disabled="savingConfig">{{ savingConfig ? '保存中…' : '保存支付配置' }}</button>
        </form>

        <div class="grid gap-6 xl:grid-cols-[minmax(0,1fr)_minmax(360px,0.8fr)]">
          <div class="card">
            <div class="flex items-center justify-between">
              <h2 class="text-lg font-medium text-gray-900 dark:text-white">支付渠道</h2>
              <button class="btn btn-secondary btn-sm" type="button" @click="startCreateProvider">新增渠道</button>
            </div>
            <div v-if="providers.length === 0" class="py-10 text-center text-sm text-gray-500">尚未配置支付渠道</div>
            <div v-else class="mt-4 space-y-3">
              <div v-for="provider in providers" :key="provider.id" class="rounded-xl border border-gray-200 p-4 dark:border-dark-600">
                <div class="flex items-start justify-between gap-4">
                  <div>
                    <p class="font-medium text-gray-900 dark:text-white">{{ provider.name }}</p>
                    <p class="mt-1 text-xs text-gray-500">{{ provider.provider_key }} · {{ provider.supported_types.join(' / ') }} · 1 {{ provider.config.currency || 'CNY' }} = {{ provider.balance_credit_rate }} USD 额度</p>
                  </div>
                  <span class="rounded-full px-2 py-1 text-xs" :class="provider.enabled ? 'bg-green-100 text-green-700' : 'bg-gray-100 text-gray-600'">{{ provider.enabled ? '已启用' : '已停用' }}</span>
                </div>
                <div class="mt-3 flex gap-2">
                  <button class="btn btn-secondary btn-sm" type="button" @click="startEditProvider(provider)">编辑</button>
                  <button class="btn btn-secondary btn-sm" type="button" @click="toggleProvider(provider)">{{ provider.enabled ? '停用' : '启用' }}</button>
                  <button class="btn btn-danger btn-sm" type="button" @click="removeProvider(provider)">删除</button>
                </div>
              </div>
            </div>
          </div>

          <form v-if="providerFormVisible" class="card space-y-4" @submit.prevent="saveProvider">
            <h2 class="text-lg font-medium text-gray-900 dark:text-white">{{ editingProviderID ? '编辑渠道' : '新增支付渠道' }}</h2>
            <label class="block text-sm">渠道适配器
              <select v-model="providerForm.providerKey" class="input mt-1 w-full" required :disabled="!!editingProviderID" @change="resetProviderAdapterFields">
                <option v-for="definition in providerDefinitions" :key="definition.key" :value="definition.key">{{ definition.name }}</option>
              </select>
            </label>
            <label class="block text-sm">按量额度兑换率
              <input v-model.number="providerForm.balanceCreditRate" class="input mt-1 w-full" type="number" min="0.00000001" step="0.00000001" required />
              <span class="mt-1 block text-xs text-gray-500">每 1 单位渠道结算货币可兑换的 USD 按量额度，例如 CNY 可填写 0.1389。</span>
            </label>
            <label class="block text-sm">名称<input v-model.trim="providerForm.name" class="input mt-1 w-full" required maxlength="100" /></label>
            <label v-for="field in selectedProviderDefinition?.config_fields || []" :key="field.key" class="block text-sm">{{ field.label }}
              <select v-if="field.kind === 'select'" v-model="providerForm.config[field.key]" class="input mt-1 w-full" :required="field.required"><option v-for="option in fieldOptions(field.options)" :key="option.value" :value="option.value">{{ option.label }}</option></select>
              <input v-else v-model.trim="providerForm.config[field.key]" class="input mt-1 w-full" :type="field.kind === 'password' ? 'password' : field.kind" :required="field.required && !(editingProviderID && field.secret)" :placeholder="editingProviderID && field.secret ? '留空表示保留现有密钥' : field.placeholder" />
            </label>
            <div class="flex flex-wrap gap-4 text-sm">
              <label v-for="method in selectedProviderDefinition?.payment_types || []" :key="method"><input v-model="providerForm.supportedTypes" type="checkbox" class="mr-1" :value="method" />{{ method }}</label>
              <label><input v-model="providerForm.enabled" type="checkbox" class="mr-1" />启用</label>
            </div>
            <div class="flex gap-2">
              <button class="btn btn-primary" type="submit" :disabled="savingProvider">{{ savingProvider ? '保存中…' : '保存渠道' }}</button>
              <button class="btn btn-secondary" type="button" @click="providerFormVisible = false">取消</button>
            </div>
          </form>
        </div>

        <div class="grid gap-6 xl:grid-cols-[minmax(0,1fr)_minmax(360px,0.8fr)]">
          <div class="card">
            <div class="flex items-center justify-between">
              <div><h2 class="text-lg font-medium text-gray-900 dark:text-white">订阅套餐</h2><p class="mt-1 text-sm text-gray-500">订单创建时固化价格、分组与有效期，后续修改不会影响旧订单。</p></div>
              <button class="btn btn-secondary btn-sm" type="button" @click="startCreatePlan">新增套餐</button>
            </div>
            <div v-if="plans.length === 0" class="py-10 text-center text-sm text-gray-500">尚未配置订阅套餐</div>
            <div v-else class="mt-4 space-y-3">
              <div v-for="plan in plans" :key="plan.id" class="rounded-xl border border-gray-200 p-4 dark:border-dark-600">
                <div class="flex items-start justify-between gap-4">
                  <div><p class="font-medium text-gray-900 dark:text-white">{{ plan.name }}</p><p class="mt-1 text-xs text-gray-500">{{ groupName(plan.group_id) }} · {{ plan.validity_days }} {{ plan.validity_unit }} · {{ formatMoney(plan.price, plan.currency) }}</p></div>
                  <span class="rounded-full px-2 py-1 text-xs" :class="plan.for_sale ? 'bg-green-100 text-green-700' : 'bg-gray-100 text-gray-600'">{{ plan.for_sale ? '在售' : '下架' }}</span>
                </div>
                <div class="mt-3 flex gap-2"><button class="btn btn-secondary btn-sm" type="button" @click="startEditPlan(plan)">编辑</button><button class="btn btn-danger btn-sm" type="button" @click="removePlan(plan)">删除</button></div>
              </div>
            </div>
          </div>

          <form v-if="planFormVisible" class="card space-y-4" @submit.prevent="savePlan">
            <h2 class="text-lg font-medium text-gray-900 dark:text-white">{{ editingPlanID ? '编辑套餐' : '新增订阅套餐' }}</h2>
            <label class="block text-sm">名称<input v-model.trim="planForm.name" class="input mt-1 w-full" required maxlength="100" /></label>
            <label class="block text-sm">订阅分组<select v-model.number="planForm.group_id" class="input mt-1 w-full" required><option :value="0" disabled>请选择</option><option v-for="group in subscriptionGroups" :key="group.id" :value="group.id">{{ group.name }}</option></select></label>
            <div class="grid grid-cols-3 gap-3"><label class="text-sm">售价<input v-model.number="planForm.price" class="input mt-1 w-full" type="number" min="0.01" step="0.01" required /></label><label class="text-sm">划线价<input v-model.number="planForm.original_price" class="input mt-1 w-full" type="number" min="0.01" step="0.01" /></label><label class="text-sm">币种<input v-model.trim="planForm.currency" class="input mt-1 w-full uppercase" minlength="3" maxlength="3" required /></label></div>
            <div class="grid grid-cols-2 gap-3"><label class="text-sm">有效期<input v-model.number="planForm.validity_days" class="input mt-1 w-full" type="number" min="1" required /></label><label class="text-sm">单位<select v-model="planForm.validity_unit" class="input mt-1 w-full"><option value="day">天</option><option value="month">月</option><option value="year">年</option></select></label></div>
            <label class="block text-sm">支付商品名<input v-model.trim="planForm.product_name" class="input mt-1 w-full" maxlength="100" /></label>
            <label class="block text-sm">说明<textarea v-model.trim="planForm.description" class="input mt-1 w-full" rows="2"></textarea></label>
            <label class="block text-sm">权益说明（支持换行）<textarea v-model.trim="planForm.features" class="input mt-1 w-full" rows="3"></textarea></label>
            <div class="flex gap-4 text-sm"><label><input v-model="planForm.for_sale" type="checkbox" class="mr-1" />在售</label><label>排序 <input v-model.number="planForm.sort_order" class="input ml-1 w-20" type="number" /></label></div>
            <div class="flex gap-2"><button class="btn btn-primary" type="submit" :disabled="savingPlan">{{ savingPlan ? '保存中…' : '保存套餐' }}</button><button class="btn btn-secondary" type="button" @click="planFormVisible = false">取消</button></div>
          </form>
        </div>

        <div class="card overflow-x-auto">
          <div class="flex items-center justify-between gap-4">
            <h2 class="text-lg font-medium text-gray-900 dark:text-white">支付订单</h2>
            <select v-model="orderStatus" class="input w-36" @change="loadOrders">
              <option value="">全部状态</option><option value="PENDING">待支付</option><option value="COMPLETED">已完成</option><option value="FAILED">入账失败</option><option value="REFUND_PENDING">退款待确认</option><option value="REFUNDED">已退款</option><option value="REFUND_FAILED">退款失败</option><option value="EXPIRED">已过期</option>
            </select>
          </div>
          <table class="mt-4 min-w-full text-left text-sm">
            <thead class="text-xs uppercase text-gray-500"><tr><th class="py-2">订单</th><th>用户</th><th>类型</th><th>金额</th><th>方式</th><th>状态</th><th>时间</th><th></th></tr></thead>
            <tbody class="divide-y divide-gray-100 dark:divide-dark-700">
              <tr v-for="order in orders" :key="order.id">
                <td class="py-3">#{{ order.id }}</td><td>{{ order.user_email || order.user_id }}</td><td>{{ order.order_type === 'subscription' ? '订阅' : '按量' }}</td><td>{{ formatMoney(order.amount, order.order_type === 'balance' ? 'USD' : order.currency) }} / {{ formatMoney(order.pay_amount, order.currency) }}</td>
                <td>{{ order.payment_type }}</td><td><span>{{ order.status }}</span><p v-if="order.refund_error" class="mt-1 max-w-xs text-xs text-amber-600">{{ order.refund_error }}</p></td><td>{{ formatDate(order.created_at || order.expires_at) }}</td>
                <td><div class="flex flex-wrap gap-2"><button v-if="['PAID', 'FAILED', 'RECHARGING'].includes(order.status)" class="btn btn-secondary btn-sm" type="button" @click="retryOrder(order)">重试入账</button><button v-if="['COMPLETED', 'REFUND_FAILED'].includes(order.status)" class="btn btn-danger btn-sm" type="button" @click="openRefund(order)">退款</button><button v-if="['REFUNDING', 'REFUND_PENDING'].includes(order.status)" class="btn btn-secondary btn-sm" type="button" @click="openResolution(order, 'refunded')">确认已退</button><button v-if="['REFUNDING', 'REFUND_PENDING'].includes(order.status)" class="btn btn-secondary btn-sm" type="button" @click="openResolution(order, 'not_refunded')">确认未退</button></div></td>
              </tr>
            </tbody>
          </table>
          <div v-if="orders.length === 0" class="py-10 text-center text-sm text-gray-500">暂无支付订单</div>
        </div>

        <div v-if="refundOrder" class="fixed inset-0 z-50 flex items-center justify-center bg-black/50 p-4" @click.self="closeRefundDialog">
          <form class="card w-full max-w-lg space-y-4" @submit.prevent="submitRefund">
            <div><h2 class="text-lg font-medium text-gray-900 dark:text-white">{{ refundResolution ? '人工确认退款结果' : `整单退款 #${refundOrder.id}` }}</h2><p class="mt-1 text-sm text-gray-500">结算退款 {{ formatMoney(refundOrder.pay_amount, refundOrder.currency) }}；{{ refundOrder.order_type === 'balance' ? `同时扣回 USD ${refundOrder.amount.toFixed(2)} 额度` : `同时扣回 ${refundOrder.subscription_days || 0} 天订阅权益` }}。</p></div>
            <template v-if="!refundResolution">
              <label class="block text-sm">处理方式<select v-model="refundForm.mode" class="input mt-1 w-full"><option value="automatic" :disabled="!canAutomaticRefund(refundOrder)">渠道自动退款</option><option value="manual">已在线下/渠道后台手动退款</option></select></label>
              <label v-if="refundForm.mode === 'manual'" class="block text-sm">外部退款凭证<input v-model.trim="refundForm.externalReference" class="input mt-1 w-full" required maxlength="200" placeholder="渠道退款单号、工单号或线下凭证" /></label>
              <label class="flex items-start gap-2 text-sm"><input v-model="refundForm.force" type="checkbox" class="mt-1" /><span>强制扣回不足的权益<span class="block text-xs text-amber-600">仅在余额已消费或订阅剩余不足且完成审核后使用，可能产生负余额或清空剩余订阅。</span></span></label>
            </template>
            <template v-else>
              <div class="rounded-lg bg-amber-50 p-3 text-sm text-amber-800">你正在确认该渠道退款{{ refundResolution === 'refunded' ? '已经成功，系统将保留已扣回权益' : '没有发生，系统将自动恢复已扣回权益' }}。</div>
              <label class="block text-sm">核验凭证<input v-model.trim="refundForm.externalReference" class="input mt-1 w-full" required maxlength="200" placeholder="渠道查询单号、工单号或审核记录" /></label>
            </template>
            <label class="block text-sm">原因与审核说明<textarea v-model.trim="refundForm.reason" class="input mt-1 w-full" required maxlength="1000" rows="3"></textarea></label>
            <div class="flex justify-end gap-2"><button class="btn btn-secondary" type="button" :disabled="savingRefund" @click="closeRefundDialog">取消</button><button class="btn btn-danger" type="submit" :disabled="savingRefund">{{ savingRefund ? '处理中…' : '确认提交' }}</button></div>
          </form>
        </div>
      </template>
    </div>
  </AppLayout>
</template>

<script setup lang="ts">
import { computed, onMounted, reactive, ref } from 'vue'
import AppLayout from '@/components/layout/AppLayout.vue'
import { useAppStore } from '@/stores'
import paymentAdminAPI, { type AdminPaymentOrder, type PaymentProvider, type PaymentProviderDefinition } from '@/api/admin/payment'
import groupsAPI from '@/api/admin/groups'
import type { PaymentConfig, SubscriptionPlan } from '@/api/payment'
import type { AdminGroup } from '@/types'

const appStore = useAppStore()
const loading = ref(true)
const savingConfig = ref(false)
const savingProvider = ref(false)
const savingPlan = ref(false)
const providers = ref<PaymentProvider[]>([])
const providerDefinitions = ref<PaymentProviderDefinition[]>([])
const plans = ref<SubscriptionPlan[]>([])
const subscriptionGroups = ref<AdminGroup[]>([])
const orders = ref<AdminPaymentOrder[]>([])
const orderStatus = ref('')
const refundOrder = ref<AdminPaymentOrder | null>(null)
const refundResolution = ref<'refunded' | 'not_refunded' | null>(null)
const savingRefund = ref(false)
const refundForm = reactive({ mode: 'automatic' as 'automatic' | 'manual', reason: '', externalReference: '', force: false })
const providerFormVisible = ref(false)
const editingProviderID = ref<number | null>(null)
const planFormVisible = ref(false)
const editingPlanID = ref<number | null>(null)
const config = reactive<PaymentConfig>({ enabled: false, min_amount: 1, max_amount: 1000, order_timeout_minutes: 5, max_pending_orders: 3, recharge_fee_rate: 0 })
const providerForm = reactive({ providerKey: '', name: '', config: {} as Record<string, string>, supportedTypes: [] as string[], balanceCreditRate: 1, enabled: false })
const planForm = reactive({ group_id: 0, name: '', description: '', price: 0, original_price: undefined as number | undefined, currency: 'CNY', validity_days: 30, validity_unit: 'day' as 'day' | 'month' | 'year', features: '', product_name: '', for_sale: true, sort_order: 0 })
const selectedProviderDefinition = computed(() => providerDefinitions.value.find(definition => definition.key === providerForm.providerKey))

async function loadAll(): Promise<void> {
  const [loadedConfig, loadedProviders, loadedDefinitions, loadedPlans, loadedGroups] = await Promise.all([paymentAdminAPI.getConfig(), paymentAdminAPI.listProviders(), paymentAdminAPI.listProviderDefinitions(), paymentAdminAPI.listPlans(), groupsAPI.getAll()])
  Object.assign(config, loadedConfig)
  providers.value = loadedProviders
  providerDefinitions.value = loadedDefinitions
  plans.value = loadedPlans
  subscriptionGroups.value = loadedGroups.filter(group => group.status === 'active' && group.subscription_type === 'subscription')
  await loadOrders()
}
async function loadOrders(): Promise<void> {
  const page = await paymentAdminAPI.listOrders(1, 50, orderStatus.value)
  orders.value = page.items as AdminPaymentOrder[]
}
async function saveConfig(): Promise<void> {
  savingConfig.value = true
  try { Object.assign(config, await paymentAdminAPI.updateConfig({ ...config })); appStore.showSuccess('支付配置已保存') }
  catch (error: any) { appStore.showError(error?.message || '保存支付配置失败') }
  finally { savingConfig.value = false }
}
function resetProviderForm(): void {
  providerForm.providerKey = providerDefinitions.value[0]?.key || ''
  providerForm.name = ''
  providerForm.balanceCreditRate = 1
  providerForm.enabled = false
  resetProviderAdapterFields()
}
function resetProviderAdapterFields(): void {
  const definition = selectedProviderDefinition.value
  const nextConfig: Record<string, string> = {}
  for (const field of definition?.config_fields || []) {
    if (field.key === 'notifyUrl') nextConfig[field.key] = `${window.location.origin}/api/v1/payment/webhook/${definition?.key}`
    else if (field.key === 'returnUrl') nextConfig[field.key] = `${window.location.origin}/purchase`
    else if (field.kind === 'select') nextConfig[field.key] = Object.keys(field.options || {})[0] || ''
    else nextConfig[field.key] = field.placeholder === 'CNY' ? 'CNY' : ''
  }
  providerForm.config = nextConfig
  providerForm.supportedTypes = [...(definition?.payment_types || [])]
}
function startCreateProvider(): void { editingProviderID.value = null; resetProviderForm(); providerFormVisible.value = true }
function startEditProvider(provider: PaymentProvider): void {
  editingProviderID.value = provider.id
  providerForm.providerKey = provider.provider_key
  providerForm.name = provider.name
  providerForm.config = { ...provider.config }
  for (const secret of provider.configured_secrets) providerForm.config[secret] = ''
  providerForm.supportedTypes = [...provider.supported_types]
  providerForm.balanceCreditRate = provider.balance_credit_rate
  providerForm.enabled = provider.enabled
  providerFormVisible.value = true
}
function providerPayload() {
  const configPayload = Object.fromEntries(Object.entries(providerForm.config).filter(([, value]) => value.trim() !== ''))
  return { provider_key: providerForm.providerKey, name: providerForm.name, config: configPayload, supported_types: providerForm.supportedTypes, balance_credit_rate: providerForm.balanceCreditRate, enabled: providerForm.enabled, sort_order: 0, limits: '' }
}
function fieldOptions(options?: Record<string, string>): Array<{ value: string; label: string }> { return Object.entries(options || {}).map(([value, label]) => ({ value, label })) }
async function saveProvider(): Promise<void> {
  savingProvider.value = true
  try {
    if (editingProviderID.value) await paymentAdminAPI.updateProvider(editingProviderID.value, providerPayload())
    else await paymentAdminAPI.createProvider(providerPayload())
    providers.value = await paymentAdminAPI.listProviders(); providerFormVisible.value = false; appStore.showSuccess('支付渠道已保存')
  } catch (error: any) { appStore.showError(error?.message || '保存支付渠道失败') }
  finally { savingProvider.value = false }
}
async function toggleProvider(provider: PaymentProvider): Promise<void> {
  try { await paymentAdminAPI.updateProvider(provider.id, { enabled: !provider.enabled }); providers.value = await paymentAdminAPI.listProviders() }
  catch (error: any) { appStore.showError(error?.message || '更新渠道失败') }
}
async function removeProvider(provider: PaymentProvider): Promise<void> {
  if (!window.confirm(`确认删除支付渠道“${provider.name}”？`)) return
  try { await paymentAdminAPI.deleteProvider(provider.id); providers.value = await paymentAdminAPI.listProviders() }
  catch (error: any) { appStore.showError(error?.message || '删除渠道失败') }
}
function resetPlanForm(): void { Object.assign(planForm, { group_id: subscriptionGroups.value[0]?.id || 0, name: '', description: '', price: 0, original_price: undefined, currency: 'CNY', validity_days: 30, validity_unit: 'day', features: '', product_name: '', for_sale: true, sort_order: 0 }) }
function startCreatePlan(): void { editingPlanID.value = null; resetPlanForm(); planFormVisible.value = true }
function startEditPlan(plan: SubscriptionPlan): void { editingPlanID.value = plan.id; Object.assign(planForm, { group_id: plan.group_id, name: plan.name, description: plan.description, price: plan.price, original_price: plan.original_price, currency: plan.currency, validity_days: plan.validity_days, validity_unit: plan.validity_unit, features: plan.features, product_name: plan.product_name, for_sale: plan.for_sale, sort_order: plan.sort_order }); planFormVisible.value = true }
async function savePlan(): Promise<void> {
  savingPlan.value = true
  try {
    const payload = { ...planForm, original_price: planForm.original_price || undefined }
    if (editingPlanID.value) await paymentAdminAPI.updatePlan(editingPlanID.value, payload)
    else await paymentAdminAPI.createPlan(payload)
    plans.value = await paymentAdminAPI.listPlans(); planFormVisible.value = false; appStore.showSuccess('订阅套餐已保存')
  } catch (error: any) { appStore.showError(error?.message || '保存订阅套餐失败') }
  finally { savingPlan.value = false }
}
async function removePlan(plan: SubscriptionPlan): Promise<void> {
  if (!window.confirm(`确认删除订阅套餐“${plan.name}”？`)) return
  try { await paymentAdminAPI.deletePlan(plan.id); plans.value = await paymentAdminAPI.listPlans() }
  catch (error: any) { appStore.showError(error?.message || '删除订阅套餐失败') }
}
function groupName(groupID: number): string { return subscriptionGroups.value.find(group => group.id === groupID)?.name || `分组 #${groupID}` }
async function retryOrder(order: AdminPaymentOrder): Promise<void> {
  try { await paymentAdminAPI.retryFulfillment(order.id); await loadOrders(); appStore.showSuccess('已执行入账重试') }
  catch (error: any) { appStore.showError(error?.message || '重试入账失败') }
}
function providerSupportsRefund(providerKey?: string): boolean { return providerDefinitions.value.some(definition => definition.key === providerKey && definition.supports_refund) }
function canAutomaticRefund(order: AdminPaymentOrder): boolean { return order.status === 'COMPLETED' && providerSupportsRefund(order.provider_key) }
function openRefund(order: AdminPaymentOrder): void { refundOrder.value = order; refundResolution.value = null; Object.assign(refundForm, { mode: canAutomaticRefund(order) ? 'automatic' : 'manual', reason: '', externalReference: '', force: false }) }
function openResolution(order: AdminPaymentOrder, outcome: 'refunded' | 'not_refunded'): void { refundOrder.value = order; refundResolution.value = outcome; Object.assign(refundForm, { reason: '', externalReference: order.refund_id || '', force: false }) }
function closeRefundDialog(): void { if (!savingRefund.value) { refundOrder.value = null; refundResolution.value = null } }
async function submitRefund(): Promise<void> {
  if (!refundOrder.value) return
  savingRefund.value = true
  let succeeded = false
  try {
    if (refundResolution.value) await paymentAdminAPI.resolveRefund(refundOrder.value.id, { outcome: refundResolution.value, reason: refundForm.reason, external_reference: refundForm.externalReference })
    else await paymentAdminAPI.requestRefund(refundOrder.value.id, { mode: refundForm.mode, reason: refundForm.reason, external_reference: refundForm.mode === 'manual' ? refundForm.externalReference : undefined, force: refundForm.force })
    await loadOrders(); appStore.showSuccess('退款状态已更新'); succeeded = true
  } catch (error: any) { appStore.showError(error?.message || '退款操作失败') }
  finally { savingRefund.value = false; if (succeeded) closeRefundDialog() }
}
function formatDate(value: string): string { return new Intl.DateTimeFormat('zh-CN', { dateStyle: 'short', timeStyle: 'short' }).format(new Date(value)) }
function formatMoney(value: number, currency = 'CNY'): string { try { return new Intl.NumberFormat('zh-CN', { style: 'currency', currency }).format(value) } catch { return `${currency} ${value.toFixed(2)}` } }

onMounted(async () => {
  try { await loadAll() } catch (error: any) { appStore.showError(error?.message || '加载支付管理数据失败') } finally { loading.value = false }
})
</script>
