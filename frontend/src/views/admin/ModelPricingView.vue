<template>
  <AppLayout>
    <div class="mx-auto max-w-7xl space-y-5 p-4 sm:p-6">
      <div class="flex flex-wrap items-start justify-between gap-3">
        <div><h1 class="text-2xl font-semibold text-gray-900 dark:text-white">{{ t('admin.modelPricing.title') }}</h1><p class="mt-1 text-sm text-gray-500">{{ t('admin.modelPricing.description') }}</p><p class="mt-1 text-xs text-gray-500">{{ t('admin.modelPricing.tokenScope') }}</p></div>
        <button class="btn btn-secondary" @click="loadHistory">{{ t('admin.modelPricing.history') }}</button>
      </div>
      <div class="rounded-lg border border-blue-200 bg-blue-50 p-4 text-sm text-blue-900 dark:border-blue-900 dark:bg-blue-950 dark:text-blue-100">
        <p>{{ t('admin.modelPricing.referenceHint') }}</p>
        <div class="mt-2 flex flex-wrap gap-4"><a href="https://platform.claude.com/docs/en/about-claude/pricing" target="_blank" rel="noopener noreferrer" class="underline">{{ t('admin.modelPricing.claudeOfficial') }}</a><a href="https://openai.com/api/pricing/" target="_blank" rel="noopener noreferrer" class="underline">{{ t('admin.modelPricing.openaiOfficial') }}</a></div>
        <p v-if="metadata" class="mt-2 text-xs">{{ sourceLabel(metadata.source) }} · {{ t('admin.modelPricing.updated') }}: {{ formatDate(metadata.updated_at) }} · {{ t('admin.modelPricing.snapshot') }}: {{ metadata.source_hash.slice(0, 12) || '—' }}</p>
      </div>
      <form class="flex flex-wrap items-center gap-3" @submit.prevent="page = 1; load()">
        <input v-model="search" class="input min-w-64 flex-1" :placeholder="t('admin.modelPricing.search')" :aria-label="t('admin.modelPricing.search')" />
        <label class="flex items-center gap-2 text-sm"><input v-model="configured" type="checkbox" @change="page = 1; load()" />{{ t('admin.modelPricing.configured') }}</label>
        <select v-model="tier" class="input w-auto" :aria-label="t('admin.modelPricing.tier')"><option value="default">Standard</option><option value="priority">Fast / Priority</option><option value="flex">Flex</option></select>
        <button class="btn btn-secondary" :disabled="loading">{{ t('common.search') }}</button>
      </form>
      <div v-if="loadError" role="alert" class="text-red-600">{{ loadError }} <button class="underline" @click="load">{{ t('admin.modelPricing.retry') }}</button></div>
      <section class="overflow-hidden rounded-xl border border-gray-200 bg-white dark:border-dark-700 dark:bg-dark-800">
        <div v-if="loading" class="p-8 text-center text-gray-500">{{ t('common.loading') }}</div>
        <div v-else class="overflow-x-auto">
          <table class="min-w-full divide-y divide-gray-200 dark:divide-dark-700">
            <thead class="bg-gray-50 text-left text-xs text-gray-500 dark:bg-dark-900"><tr><th class="px-4 py-3">{{ t('admin.modelPricing.model') }}</th><th v-for="field in standardFields" :key="field" class="px-4 py-3 whitespace-nowrap">{{ fieldLabel(field) }}<div class="mt-1 font-normal">{{ t('admin.modelPricing.priceColumns') }}</div></th><th class="px-4 py-3">{{ t('common.actions') }}</th></tr></thead>
            <tbody class="divide-y divide-gray-100 dark:divide-dark-700">
              <tr v-for="row in items" :key="row.model" class="text-sm text-gray-700 dark:text-gray-200">
                <td class="px-4 py-4"><div class="font-mono">{{ row.model }}</div><div v-if="row.alias" class="mt-1 text-xs text-gray-500">{{ t('admin.modelPricing.alias') }} → {{ row.alias }}</div><div class="mt-1 text-xs text-gray-500">{{ row.available ? `${sourceLabel(row.source)} · ${row.resolved_model}` : t('admin.modelPricing.unavailable') }}</div><span v-if="row.recent" class="mr-1 mt-1 inline-block rounded bg-blue-100 px-2 text-xs text-blue-800">{{ t('admin.modelPricing.recent') }}</span><span v-if="Object.keys(row.override).length" class="mt-1 inline-block rounded bg-amber-100 px-2 text-xs text-amber-800">{{ t('admin.modelPricing.custom') }}</span></td>
                <td v-for="field in standardFields" :key="field" class="px-4 py-4 font-mono whitespace-nowrap"><span class="text-gray-400">{{ displayPrice(row.tiers?.[tier]?.reference[field]) }}</span><span class="mx-1">→</span><span :class="row.tiers?.[tier]?.effective[field] !== row.tiers?.[tier]?.reference[field] ? 'text-amber-600 dark:text-amber-400' : ''">{{ displayPrice(row.tiers?.[tier]?.effective[field]) }}</span></td>
                <td class="px-4 py-4"><button class="btn btn-secondary btn-sm" :disabled="row.mode === 'image_generation'" @click="edit(row)">{{ t('common.edit') }}</button></td>
              </tr>
              <tr v-if="!items.length"><td colspan="7" class="p-8 text-center text-gray-500">{{ t('admin.modelPricing.empty') }}</td></tr>
            </tbody>
          </table>
        </div>
      </section>
      <div class="flex items-center justify-between text-sm text-gray-500"><span>{{ t('admin.modelPricing.total', { count: total }) }} · {{ t('admin.modelPricing.unit') }}</span><div class="flex items-center gap-3"><button class="btn btn-secondary btn-sm" :disabled="page <= 1 || loading" @click="page--; load()">{{ t('admin.modelPricing.previous') }}</button><span>{{ page }} / {{ Math.max(1, Math.ceil(total / 50)) }}</span><button class="btn btn-secondary btn-sm" :disabled="page * 50 >= total || loading" @click="page++; load()">{{ t('admin.modelPricing.next') }}</button></div></div>

      <BaseDialog :show="!!selected" :title="`${t('admin.modelPricing.edit')} · ${selected?.model || ''}`" width="extra-wide" @close="closeEditor">
        <div v-if="selected" class="space-y-5">
          <p class="text-sm text-gray-500">{{ t('admin.modelPricing.overrideHint') }}</p>
          <div><label class="input-label" for="price-alias">{{ t('admin.modelPricing.alias') }}</label><input id="price-alias" v-model="alias" class="input font-mono" :placeholder="t('admin.modelPricing.aliasHint')" /><p class="mt-1 text-xs text-gray-500">{{ t('admin.modelPricing.aliasScope') }}</p></div>
          <div class="overflow-x-auto rounded-lg border border-gray-200 dark:border-dark-700">
            <table class="w-full text-sm"><thead class="text-left text-gray-500"><tr><th class="p-2">{{ t('admin.modelPricing.unit') }}</th><th class="p-2">{{ t('admin.modelPricing.referencePrice') }}</th><th class="p-2">{{ currentPreview ? t('admin.modelPricing.candidatePrice') : t('admin.modelPricing.effectivePrice') }}</th></tr></thead><tbody><tr v-for="field in standardFields" :key="field"><td class="p-2">{{ fieldLabel(field) }}</td><td class="p-2 font-mono">{{ displayPrice(currentPreview?.unit_prices?.reference[field] ?? selected.tiers?.[previewTier]?.reference[field]) }}</td><td class="p-2 font-mono">{{ displayPrice(currentPreview?.unit_prices?.effective[field] ?? selected.tiers?.[previewTier]?.effective[field]) }}</td></tr></tbody></table>
          </div>
          <div class="grid gap-3 sm:grid-cols-2">
            <div v-for="field in priceFields" :key="field"><label class="input-label" :for="`price-${field}`">{{ fieldLabel(field) }}</label><input :id="`price-${field}`" v-model="priceInputs[field]" type="number" min="0" max="1000000" step="any" class="input" :placeholder="t('admin.modelPricing.inherit')" /></div>
          </div>
          <p class="text-xs text-gray-500">{{ t('admin.modelPricing.priorityHint') }}</p>
          <p v-if="selected.effective?.long_context_threshold" class="text-xs text-gray-500">{{ t('admin.modelPricing.longContext', { threshold: selected.effective.long_context_threshold, input: selected.effective.long_context_input_multiplier, output: selected.effective.long_context_output_multiplier, read: selected.effective.long_context_cache_read_multiplier, write: selected.effective.long_context_cache_write_multiplier }) }}</p>
          <button class="text-sm text-primary-600 underline" @click="clearOverrides">{{ t('admin.modelPricing.reset') }}</button>
          <div class="border-t border-gray-200 pt-4 dark:border-dark-700">
            <h3 class="font-medium">{{ t('admin.modelPricing.preview') }}</h3><p class="mt-1 text-xs text-gray-500">{{ t('admin.modelPricing.previewHint') }}</p>
            <div class="mt-3 grid gap-3 sm:grid-cols-2"><div v-for="field in standardFields" :key="field"><label class="input-label" :for="`tokens-${field}`">{{ fieldLabel(field) }} tokens</label><input :id="`tokens-${field}`" v-model.number="tokens[field]" type="number" min="0" max="1000000000" step="1" class="input" /></div></div>
            <div class="mt-3 flex flex-wrap gap-3"><select v-model="previewTier" class="input w-auto" :aria-label="t('admin.modelPricing.tier')"><option value="default">Standard</option><option value="priority">Fast / Priority</option><option value="flex">Flex</option></select><select v-model="subscription" class="input w-auto" :aria-label="t('admin.modelPricing.billingMode')"><option :value="true">{{ t('admin.modelPricing.subscription') }}</option><option :value="false">{{ t('admin.modelPricing.balance') }}</option></select></div>
            <div class="mt-3 grid gap-3 sm:grid-cols-3"><div v-for="field in factorFields" :key="field"><label class="input-label" :for="`factor-${field}`">{{ t(`admin.modelPricing.${field}`) }}</label><input :id="`factor-${field}`" v-model.number="factors[field]" type="number" :min="field === 'group_rate' ? 0.0001 : 0" :max="field === 'user_discount' ? 1 : 100" step="0.0001" class="input" :disabled="subscription" /></div></div>
            <p v-if="subscription" class="mt-2 text-xs text-gray-500">{{ t('admin.modelPricing.subscriptionHint') }}</p>
            <button class="btn btn-secondary mt-3" :disabled="previewing || saving" @click="runPreview">{{ previewing ? t('common.loading') : t('admin.modelPricing.calculate') }}</button>
            <div v-if="currentPreview" class="mt-3 rounded-lg bg-gray-50 p-4 text-sm dark:bg-dark-900"><dl class="grid gap-2 sm:grid-cols-2"><div>{{ t('admin.modelPricing.referenceCost') }}: <strong>{{ money(currentPreview.cost.pricing_snapshot.reference_total_cost) }}</strong></div><div>{{ t('admin.modelPricing.siteCost') }}: <strong>{{ money(currentPreview.cost.total_cost) }}</strong></div><div>{{ t('admin.modelPricing.charged') }}: <strong>{{ money(currentPreview.charged_amount) }}</strong></div><div>{{ t('admin.modelPricing.resolved') }}: <span class="font-mono">{{ currentPreview.cost.pricing_snapshot.resolved_model }}</span></div></dl></div>
            <p v-else class="mt-3 text-xs text-gray-500">{{ t('admin.modelPricing.reviewHint') }}</p>
          </div>
        </div>
        <template #footer><button class="btn btn-secondary" :disabled="saving" @click="closeEditor">{{ t('common.cancel') }}</button><button class="btn btn-primary" :disabled="saving || !currentPreview" @click="save">{{ saving ? t('common.saving') : t('common.save') }}</button></template>
      </BaseDialog>
      <BaseDialog :show="showHistory" :title="t('admin.modelPricing.history')" width="extra-wide" @close="showHistory = false">
        <div v-if="!edits.length" class="text-sm text-gray-500">{{ t('admin.modelPricing.emptyHistory') }}</div>
        <div v-for="(change, index) in edits" :key="index" class="mb-4 border-b pb-3 text-sm dark:border-dark-700"><div class="font-mono">{{ change.model }}</div><div class="mt-1 text-xs text-gray-500">{{ formatDate(change.at) }} · {{ t('admin.modelPricing.actor') }} {{ change.actor_id }}</div><div class="mt-2">{{ t('admin.modelPricing.alias') }}: {{ change.before_alias || '—' }} → {{ change.after_alias || '—' }}</div><div v-for="field in changedFields(change)" :key="field" class="mt-1">{{ fieldLabel(field) }}: {{ change.before[field] ?? t('admin.modelPricing.inherit') }} → {{ change.after[field] ?? t('admin.modelPricing.inherit') }}</div></div>
      </BaseDialog>
    </div>
  </AppLayout>
</template>

<script setup lang="ts">
import { computed, onMounted, reactive, ref } from 'vue'
import { useI18n } from 'vue-i18n'
import { adminAPI } from '@/api/admin'
import { priceFields, type PriceOverride, type ModelPrice, type PricingMetadata, type PriceEdit, type PricePreview, type PreviewInput } from '@/api/admin/modelPricing'
import { useAppStore } from '@/stores/app'
import AppLayout from '@/components/layout/AppLayout.vue'
import BaseDialog from '@/components/common/BaseDialog.vue'

const { t } = useI18n()
const appStore = useAppStore()
const standardFields = priceFields.slice(0, 5)
const factorFields = ['group_rate', 'model_rate', 'user_discount'] as const
const items = ref<ModelPrice[]>([])
const metadata = ref<PricingMetadata | null>(null)
const search = ref('')
const configured = ref(false)
const tier = ref('default')
const page = ref(1)
const total = ref(0)
const loading = ref(false)
const loadError = ref('')
const selected = ref<ModelPrice | null>(null)
const alias = ref('')
const priceInputs = reactive<Record<string, string | number>>({})
const tokens = reactive<Record<string, number>>({ input: 1000, output: 1000, cache_read: 1000, cache_write_5m: 1000, cache_write_1h: 1000 })
const factors = reactive({ group_rate: 1, model_rate: 1, user_discount: 1 })
const subscription = ref(true)
const previewTier = ref('default')
const previewing = ref(false)
const saving = ref(false)
const result = ref<PricePreview | null>(null)
const previewSignature = ref('')
const showHistory = ref(false)
const edits = ref<PriceEdit[]>([])
const signature = computed(() => JSON.stringify([selected.value?.model, alias.value, priceInputs, tokens, factors, subscription.value, previewTier.value]))
const currentPreview = computed(() => previewSignature.value === signature.value ? result.value : null)
const fieldLabel = (field: string) => t(`admin.modelPricing.fields.${field}`)
const sourceLabel = (source: string) => t(`admin.modelPricing.sources.${source || 'unknown'}`)
const displayPrice = (value?: number) => value == null ? '—' : `$${Number(value.toPrecision(8))}`
const money = (value: number) => `$${value.toFixed(8)}`
const formatDate = (value: string) => value && !value.startsWith('0001') ? new Date(value).toLocaleString() : '—'
const changedFields = (change: PriceEdit) => priceFields.filter(field => change.before[field] !== change.after[field])
const errorMessage = (error: unknown) => (error as { response?: { data?: { message?: string; detail?: string } } }).response?.data?.detail || (error as { response?: { data?: { message?: string } } }).response?.data?.message || t('admin.modelPricing.failed')
let loadGeneration = 0
async function load() {
  const generation = ++loadGeneration
  loading.value = true
  loadError.value = ''
  try { const data = await adminAPI.modelPricing.list({ search: search.value, configured: configured.value, page: page.value }); if (generation !== loadGeneration) return; items.value = data.items; total.value = data.total; metadata.value = data.metadata }
  catch (error) { if (generation === loadGeneration) loadError.value = errorMessage(error) }
  finally { if (generation === loadGeneration) loading.value = false }
}
function edit(row: ModelPrice) {
  selected.value = row
  alias.value = row.alias || ''
  previewTier.value = tier.value
  for (const field of priceFields) priceInputs[field] = row.override[field] ?? ''
  result.value = null
  previewSignature.value = ''
}
function closeEditor() { if (!saving.value) { selected.value = null; result.value = null } }
function clearOverrides() { for (const field of priceFields) priceInputs[field] = '' }
function overrides(): PriceOverride {
  const out: PriceOverride = {}
  for (const field of priceFields) {
    const input = priceInputs[field]
    if (input === '' || input == null) continue
    const value = Number(input)
    if (!Number.isFinite(value) || value < 0 || value > 1000000) throw new Error(t('admin.modelPricing.invalidPrice'))
    out[field] = value
  }
  return out
}
function previewInput(): PreviewInput {
  return { model: selected.value!.model, alias: alias.value.trim(), override: overrides(), service_tier: previewTier.value, subscription: subscription.value, ...(subscription.value ? { group_rate: 1, model_rate: 1, user_discount: 1 } : factors),
    tokens: { input_tokens: tokens.input, output_tokens: tokens.output, cache_read_tokens: tokens.cache_read, cache_creation_5m_tokens: tokens.cache_write_5m, cache_creation_1h_tokens: tokens.cache_write_1h } }
}
async function runPreview() {
  if (!selected.value) return
  previewing.value = true
  const startedSignature = signature.value
  try { const data = await adminAPI.modelPricing.preview(previewInput()); if (signature.value === startedSignature) { result.value = data; previewSignature.value = startedSignature } }
  catch (error) { appStore.showError(error instanceof Error && !('response' in error) ? error.message : errorMessage(error)) }
  finally { previewing.value = false }
}
async function save() {
  if (!selected.value || !currentPreview.value) return
  saving.value = true
  try { await adminAPI.modelPricing.update({ model: selected.value.model, alias: alias.value.trim(), override: overrides(), expected_version: selected.value.version }); selected.value = null; result.value = null; appStore.showSuccess(t('admin.modelPricing.saved')); await load() }
  catch (error) { appStore.showError(errorMessage(error)); result.value = null; if ((error as { response?: { status?: number } }).response?.status === 409) { await load(); try { const fresh = await adminAPI.modelPricing.list({ search: selected.value!.model, configured: false, page: 1 }); const row = fresh.items.find(item => item.model === selected.value?.model); if (row) selected.value = row } catch (reloadError) { appStore.showError(errorMessage(reloadError)) } } }
  finally { saving.value = false }
}
async function loadHistory() {
  try { edits.value = (await adminAPI.modelPricing.history()).reverse(); showHistory.value = true }
  catch (error) { appStore.showError(errorMessage(error)) }
}
onMounted(load)
</script>
