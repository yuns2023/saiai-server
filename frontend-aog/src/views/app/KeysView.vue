<script setup lang="ts">
import { computed, onMounted, ref } from 'vue'
import { useRoute, useRouter } from 'vue-router'
import { useI18n } from 'vue-i18n'
import ConfirmDialog from '@/components/ConfirmDialog.vue'
import ConnectPanel from '@/components/ConnectPanel.vue'
import CopyButton from '@/components/CopyButton.vue'
import Icon from '@/components/Icon.vue'
import KeyForm, { type KeyFormValue } from '@/components/KeyForm.vue'
import Pager from '@/components/Pager.vue'
import { groupsApi, keysApi } from '@/api'
import { errorMessage } from '@/api/client'
import { intlLocale } from '@/i18n'
import { dateOnly, maskKey, money, rate, shortTime } from '@/lib/format'
import { useAppStore } from '@/stores/app'
import type { ApiKey, CreateApiKeyRequest, Group, UpdateApiKeyRequest } from '@/types'

const { t } = useI18n()
const app = useAppStore()
const route = useRoute()
const router = useRouter()

const keys = ref<ApiKey[]>([])
const page = ref(1)
const pages = ref(1)
const total = ref(0)
const PAGE_SIZE = 20
const groups = ref<Group[]>([])
const rates = ref<Record<number, number>>({})
const loading = ref(true)
const failed = ref(false)
const creating = ref(route.query.new === '1')
const editing = ref<number | null>(null)
const connecting = ref<number | null>(null)
const revealed = ref<Set<number>>(new Set())
const busy = ref(false)
const toDelete = ref<ApiKey | null>(null)
const justCreated = ref<number | null>(null)

const groupById = computed(() => new Map(groups.value.map((g) => [g.id, g])))
const platformOf = (k: ApiKey) => k.group?.platform ?? (k.group_id ? groupById.value.get(k.group_id)?.platform : undefined) ?? 'anthropic'
const platformLabel: Record<string, string> = { anthropic: 'Anthropic', openai: 'OpenAI', gemini: 'Google', antigravity: 'Google', sora: 'OpenAI' }

let listSeq = 0
async function loadKeys(p = page.value): Promise<void> {
  const seq = ++listSeq
  const list = await keysApi.list(p, PAGE_SIZE)
  if (seq !== listSeq) return // a newer page request superseded this one
  keys.value = list.items
  page.value = list.page || p
  pages.value = list.pages || 1
  total.value = list.total || 0
}

async function load(): Promise<void> {
  loading.value = true
  failed.value = false
  try {
    const [, gs, rs] = await Promise.all([loadKeys(), groupsApi.available(), groupsApi.rates().catch(() => ({}))])
    groups.value = gs
    rates.value = rs
  } catch {
    failed.value = true
  } finally {
    loading.value = false
  }
}
onMounted(load)

async function goPage(p: number): Promise<void> {
  editing.value = null
  connecting.value = null
  try {
    await loadKeys(p)
  } catch (e) {
    app.notify('error', errorMessage(e, t('common.loadFailed')))
  }
}

function lines(s: string): string[] {
  return s.split(/[\n,]/).map((x) => x.trim()).filter(Boolean)
}

async function create(v: KeyFormValue): Promise<void> {
  busy.value = true
  try {
    const req: CreateApiKeyRequest = { name: v.name, group_id: v.group_id }
    if (v.quota) req.quota = v.quota
    if (v.expires_in_days) req.expires_in_days = v.expires_in_days
    if (v.max_concurrency) req.max_concurrency = v.max_concurrency
    if (v.rate_limit_5h) req.rate_limit_5h = v.rate_limit_5h
    if (v.rate_limit_1d) req.rate_limit_1d = v.rate_limit_1d
    if (v.rate_limit_7d) req.rate_limit_7d = v.rate_limit_7d
    if (lines(v.ip_whitelist).length) req.ip_whitelist = lines(v.ip_whitelist)
    if (lines(v.ip_blacklist).length) req.ip_blacklist = lines(v.ip_blacklist)
    const key = await keysApi.create(req)
    // Newest first: show page 1 so the new key is the row we open below.
    await loadKeys(1).catch(() => (keys.value = [key, ...keys.value]))
    if (!keys.value.some((k) => k.id === key.id)) keys.value = [key, ...keys.value]
    creating.value = false
    justCreated.value = key.id
    connecting.value = key.id
    revealed.value = new Set([...revealed.value, key.id])
    if (route.query.new) router.replace({ query: {} })
    app.notify('ok', t('keys.created'))
  } catch (e) {
    app.notify('error', errorMessage(e, t('keys.createFailed')))
  } finally {
    busy.value = false
  }
}

async function save(k: ApiKey, v: KeyFormValue): Promise<void> {
  busy.value = true
  try {
    const req: UpdateApiKeyRequest = {
      name: v.name,
      group_id: v.group_id,
      quota: v.quota ?? 0,
      rate_limit_5h: v.rate_limit_5h ?? 0,
      rate_limit_1d: v.rate_limit_1d ?? 0,
      rate_limit_7d: v.rate_limit_7d ?? 0,
      ip_whitelist: lines(v.ip_whitelist),
      ip_blacklist: lines(v.ip_blacklist)
    }
    if (v.max_concurrency) req.max_concurrency = v.max_concurrency
    const updated = await keysApi.update(k.id, req)
    keys.value = keys.value.map((x) => (x.id === k.id ? updated : x))
    editing.value = null
    app.notify('ok', t('common.saved'))
  } catch (e) {
    app.notify('error', errorMessage(e, t('common.saveFailed')))
  } finally {
    busy.value = false
  }
}

async function toggle(k: ApiKey): Promise<void> {
  const status = k.status === 'active' ? 'inactive' : 'active'
  try {
    // PUT /keys/:id always overwrites both IP lists (a missing field clears
    // them), so a status-only update must resend the current lists.
    const updated = await keysApi.update(k.id, { status, ip_whitelist: k.ip_whitelist ?? [], ip_blacklist: k.ip_blacklist ?? [] })
    keys.value = keys.value.map((x) => (x.id === k.id ? updated : x))
  } catch (e) {
    app.notify('error', errorMessage(e, t('common.saveFailed')))
  }
}

async function confirmDelete(): Promise<void> {
  const k = toDelete.value
  if (!k) return
  busy.value = true
  try {
    await keysApi.remove(k.id)
    keys.value = keys.value.filter((x) => x.id !== k.id)
    await loadKeys(keys.value.length || page.value === 1 ? page.value : page.value - 1).catch(() => undefined)
    toDelete.value = null
    app.notify('ok', t('keys.deleted', { name: k.name }))
  } catch (e) {
    app.notify('error', errorMessage(e, t('keys.deleteFailed')))
  } finally {
    busy.value = false
  }
}

function reveal(id: number): void {
  const next = new Set(revealed.value)
  if (next.has(id)) next.delete(id)
  else next.add(id)
  revealed.value = next
}

function statusTag(k: ApiKey): { cls: string; text: string } {
  switch (k.status) {
    case 'active':
      return { cls: 'tag-ok', text: t('keys.status_active') }
    case 'inactive':
      return { cls: 'tag-off', text: t('keys.status_inactive') }
    case 'quota_exhausted':
      return { cls: 'tag-warn', text: t('keys.status_quota_exhausted') }
    default:
      return { cls: 'tag-bad', text: t('keys.status_expired') }
  }
}

function openRow(kind: 'edit' | 'connect', id: number): void {
  if (kind === 'edit') {
    editing.value = editing.value === id ? null : id
    connecting.value = null
  } else {
    connecting.value = connecting.value === id ? null : id
    editing.value = null
  }
}
</script>

<template>
  <section aria-labelledby="keys-title">
    <div class="flex flex-wrap items-start justify-between gap-4">
      <div>
        <h1 id="keys-title" class="text-lg font-semibold tracking-tight">{{ t('keys.title') }}</h1>
        <p class="mt-1 text-sm text-ink-2">{{ t('keys.lead') }}</p>
      </div>
      <button v-if="!creating" type="button" class="btn btn-primary" :disabled="loading || !groups.length" @click="creating = true">
        <Icon name="plus" :size="16" />{{ t('keys.new') }}
      </button>
    </div>

    <div v-if="creating" class="rule-double mt-6 border-t border-rule-strong pb-6 pt-5">
      <h2 class="mb-4 text-md font-semibold">{{ t('keys.new') }}</h2>
      <p v-if="!loading && !groups.length" class="text-sm text-ink-2">{{ t('keys.noGroups') }}</p>
      <KeyForm v-else-if="!loading" :groups="groups" :rates="rates" :busy="busy" @submit="create" @cancel="creating = false; router.replace({ query: {} })" />
    </div>

    <div class="mt-6">
      <p v-if="failed" class="py-6 text-sm text-seal" role="alert">
        {{ t('common.loadFailed') }} <button type="button" class="btn btn-quiet btn-sm" @click="load">{{ t('common.retry') }}</button>
      </p>
      <div v-else-if="loading" class="space-y-3 pt-2">
        <div v-for="i in 3" :key="i" class="skeleton h-10" />
      </div>
      <div v-else-if="!keys.length && !creating" class="border-y border-rule py-10 text-center">
        <p class="text-sm text-ink-2">{{ t('keys.empty') }}</p>
        <button type="button" class="btn btn-primary mt-4" :disabled="!groups.length" @click="creating = true">{{ t('keys.new') }}</button>
      </div>

      <ul v-else class="border-t border-rule-strong">
        <li v-for="k in keys" :key="k.id" class="border-b border-rule" :class="justCreated === k.id && 'bg-blue-wash/50'">
          <div class="grid grid-cols-1 gap-x-6 gap-y-2 py-4 md:grid-cols-[minmax(9rem,1.2fr)_minmax(14rem,2fr)_minmax(9rem,1fr)_auto] md:items-center">
            <div class="min-w-0">
              <div class="flex items-center gap-2">
                <span class="truncate font-medium">{{ k.name }}</span>
                <span class="tag" :class="statusTag(k).cls">{{ statusTag(k).text }}</span>
              </div>
              <div class="mt-0.5 text-xs text-ink-3">
                {{ k.group?.name ?? groupById.get(k.group_id ?? -1)?.name ?? '—' }} · {{ platformLabel[platformOf(k)] ?? platformOf(k) }}
                <template v-if="k.group"> · {{ rate(rates[k.group.id] ?? k.group.rate_multiplier) }}</template>
              </div>
            </div>

            <div class="flex min-w-0 items-center gap-2">
              <code class="min-w-0 truncate font-mono text-xs text-ink-2">{{ revealed.has(k.id) ? k.key : maskKey(k.key) }}</code>
              <button type="button" class="shrink-0 text-ink-3 hover:text-ink" :aria-label="revealed.has(k.id) ? t('keys.hide') : t('keys.show')" @click="reveal(k.id)">
                <Icon :name="revealed.has(k.id) ? 'eyeOff' : 'eye'" :size="16" />
              </button>
              <CopyButton :value="k.key" small class="shrink-0" />
            </div>

            <dl class="grid grid-cols-2 gap-x-4 text-xs md:block md:space-y-0.5">
              <div class="flex gap-1.5">
                <dt class="text-ink-3">{{ t('keys.used') }}</dt>
                <dd class="num">{{ money(k.quota_used, 2) }}<template v-if="k.quota"> / {{ money(k.quota, 2) }}</template></dd>
              </div>
              <div class="flex gap-1.5">
                <dt class="text-ink-3">{{ t('keys.lastUsed') }}</dt>
                <dd class="num">{{ k.last_used_at ? shortTime(k.last_used_at, intlLocale()) : t('keys.never') }}</dd>
              </div>
              <div v-if="k.expires_at" class="flex gap-1.5">
                <dt class="text-ink-3">{{ t('keys.expires') }}</dt>
                <dd class="num">{{ dateOnly(k.expires_at, intlLocale()) }}</dd>
              </div>
            </dl>

            <div class="flex flex-wrap items-center gap-1 md:justify-end">
              <button type="button" class="btn btn-sm" :class="connecting === k.id ? 'btn-primary' : 'btn-line'" :aria-expanded="connecting === k.id" @click="openRow('connect', k.id)">
                {{ t('keys.connect') }}
              </button>
              <button type="button" class="btn btn-quiet btn-sm" :aria-expanded="editing === k.id" @click="openRow('edit', k.id)">{{ t('common.edit') }}</button>
              <button v-if="k.status === 'active' || k.status === 'inactive'" type="button" class="btn btn-quiet btn-sm" @click="toggle(k)">
                {{ k.status === 'active' ? t('keys.disable') : t('keys.enable') }}
              </button>
              <button type="button" class="btn btn-quiet btn-sm !text-seal hover:!bg-seal-wash" @click="toDelete = k">{{ t('common.delete') }}</button>
            </div>
          </div>

          <div v-if="connecting === k.id" class="border-t border-rule-soft pb-6 pt-4">
            <p v-if="justCreated === k.id" class="mb-4 text-sm text-ink">{{ t('keys.createdNext') }}</p>
            <ConnectPanel :api-key="k.key" :platform="platformOf(k)" />
          </div>
          <div v-if="editing === k.id" class="border-t border-rule-soft pb-6 pt-4">
            <KeyForm :groups="groups" :rates="rates" :initial="k" :busy="busy" @submit="save(k, $event)" @cancel="editing = null" />
          </div>
        </li>
      </ul>
      <Pager v-if="!loading && total > 0" :page="page" :pages="pages" :total="total" @go="goPage" />
    </div>

    <ConfirmDialog
      :open="!!toDelete"
      :title="t('keys.deleteTitle', { name: toDelete?.name ?? '' })"
      :body="t('keys.deleteBody')"
      :confirm-label="t('common.delete')"
      danger
      :busy="busy"
      @confirm="confirmDelete"
      @cancel="toDelete = null"
    />
  </section>
</template>
