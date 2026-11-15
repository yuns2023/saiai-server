<script setup lang="ts">
import { computed, onMounted, ref } from 'vue'
import { useI18n } from 'vue-i18n'
import ConfirmDialog from '@/components/ConfirmDialog.vue'
import CopyButton from '@/components/CopyButton.vue'
import Icon from '@/components/Icon.vue'
import Pager from '@/components/Pager.vue'
import { invitesApi } from '@/api'
import { errorMessage, type ApiError } from '@/api/client'
import { intlLocale } from '@/i18n'
import { shortTime } from '@/lib/format'
import { useAppStore } from '@/stores/app'
import type { UserInviteCode, UserInviteOverview } from '@/types'

const { t } = useI18n()
const app = useAppStore()

const PAGE_SIZE = 20
const data = ref<UserInviteOverview | null>(null)
const page = ref(1)
const loading = ref(true)
const failed = ref(false)
const busy = ref(false)
const toRevoke = ref<UserInviteCode | null>(null)
const justCreated = ref('')

const pages = computed(() => data.value?.pagination?.pages || 1)
const total = computed(() => data.value?.pagination?.total || 0)

/** Why the generate button is off, in the order the server checks. */
const blocker = computed(() => {
  const d = data.value
  if (!d || d.can_create) return ''
  if (!d.enabled) return t('invites.disabled')
  if (!d.eligible) return t('invites.notEligible')
  if (d.unused >= d.max_unused) return t('invites.limitUnused')
  if (d.max_total > 0 && d.invited + d.unused >= d.max_total) return t('invites.limitTotal')
  return ''
})

let seq = 0
async function load(p = page.value): Promise<void> {
  const mine = ++seq
  failed.value = false
  try {
    const res = await invitesApi.overview(p, PAGE_SIZE)
    if (mine !== seq) return
    data.value = res
    page.value = res.pagination?.page || p
  } catch {
    if (mine === seq) failed.value = true
  } finally {
    if (mine === seq) loading.value = false
  }
}
onMounted(() => load(1))

function signupLink(code: string): string {
  return `${window.location.origin}/register?invite=${encodeURIComponent(code)}`
}

function reasonText(e: unknown, fallback: string): string {
  switch ((e as Partial<ApiError>)?.reason) {
    case 'USER_INVITE_DISABLED':
      return t('invites.disabled')
    case 'USER_INVITE_NOT_ELIGIBLE':
      return t('invites.notEligible')
    case 'USER_INVITE_UNUSED_LIMIT':
      return t('invites.limitUnused')
    case 'USER_INVITE_TOTAL_LIMIT':
      return t('invites.limitTotal')
    case 'USER_INVITE_DAILY_LIMIT':
      return t('invites.limitDaily')
    default:
      return errorMessage(e, fallback)
  }
}

async function create(): Promise<void> {
  busy.value = true
  try {
    const created = await invitesApi.create()
    justCreated.value = created.code
    await load(1)
    app.notify('ok', t('invites.created'))
  } catch (e) {
    app.notify('error', reasonText(e, t('invites.createFailed')))
    await load().catch(() => undefined)
  } finally {
    busy.value = false
  }
}

async function confirmRevoke(): Promise<void> {
  const c = toRevoke.value
  if (!c) return
  busy.value = true
  try {
    await invitesApi.revoke(c.code)
    toRevoke.value = null
    await load()
    app.notify('ok', t('invites.revoked'))
  } catch (e) {
    toRevoke.value = null
    app.notify('error', reasonText(e, t('invites.revokeFailed')))
    await load().catch(() => undefined)
  } finally {
    busy.value = false
  }
}

function statusTag(c: UserInviteCode): { cls: string; text: string } {
  if (c.status === 'unused') return { cls: 'tag-ok', text: t('invites.status_unused') }
  if (c.status === 'used') return { cls: 'tag-off', text: t('invites.status_used') }
  return { cls: 'tag-bad', text: t('invites.status_expired') }
}
</script>

<template>
  <section aria-labelledby="invites-title">
    <div class="flex flex-wrap items-start justify-between gap-4">
      <div>
        <h1 id="invites-title" class="text-lg font-semibold tracking-tight">{{ t('invites.title') }}</h1>
        <p class="mt-1 text-sm text-ink-2">{{ t('invites.lead') }}</p>
      </div>
      <button type="button" class="btn btn-primary" :disabled="loading || busy || !data?.can_create" @click="create">
        <Icon name="plus" :size="16" />{{ t('invites.new') }}
      </button>
    </div>

    <div v-if="data" class="rule-double mt-6 flex flex-wrap items-center gap-x-6 gap-y-1 border-b pb-3 text-sm" data-testid="invite-summary">
      <span class="num">{{ t('invites.unusedOf', { n: data.unused, max: data.max_unused }) }}</span>
      <span class="num">{{ data.max_total > 0 ? t('invites.invitedOf', { n: data.invited, max: data.max_total }) : t('invites.invited', { n: data.invited }) }}</span>
    </div>
    <p v-if="blocker" class="mt-3 text-sm text-ink-2" role="status" data-testid="invite-blocker">
      {{ blocker }}
      <RouterLink v-if="data && data.enabled && !data.eligible" to="/billing" class="ml-1">{{ t('invites.goTopUp') }}</RouterLink>
    </p>

    <div class="mt-4">
      <p v-if="failed" class="py-6 text-sm text-seal" role="alert">
        {{ t('common.loadFailed') }} <button type="button" class="btn btn-quiet btn-sm" @click="load()">{{ t('common.retry') }}</button>
      </p>
      <div v-else-if="loading" class="space-y-3 pt-2">
        <div v-for="i in 3" :key="i" class="skeleton h-10" />
      </div>
      <p v-else-if="!data?.codes.length" class="border-y border-rule py-10 text-center text-sm text-ink-2">{{ t('invites.empty') }}</p>
      <ul v-else class="border-t border-rule-strong">
        <li
          v-for="c in data.codes"
          :key="c.code"
          class="grid grid-cols-1 gap-x-6 gap-y-2 border-b border-rule py-3 md:grid-cols-[minmax(16rem,2fr)_minmax(10rem,1.2fr)_auto] md:items-center"
          :class="justCreated === c.code && 'bg-blue-wash/50'"
        >
          <div class="flex min-w-0 items-center gap-2">
            <span class="tag shrink-0" :class="statusTag(c).cls">{{ statusTag(c).text }}</span>
            <code class="min-w-0 truncate font-mono text-xs" :class="c.status === 'unused' ? 'text-ink' : 'text-ink-3 line-through decoration-ink-3/40'">{{ c.code }}</code>
          </div>
          <div class="text-xs text-ink-3">
            <template v-if="c.status === 'used' && c.invitee_email">{{ t('invites.usedBy', { email: c.invitee_email }) }} · {{ shortTime(c.used_at, intlLocale()) }}</template>
            <template v-else>{{ t('invites.createdAt', { time: shortTime(c.created_at, intlLocale()) }) }}</template>
          </div>
          <div v-if="c.status === 'unused'" class="flex flex-wrap items-center gap-1 md:justify-end">
            <CopyButton :value="c.code" small />
            <CopyButton :value="signupLink(c.code)" :label="t('invites.copyLink')" small />
            <button type="button" class="btn btn-quiet btn-sm !text-seal hover:!bg-seal-wash" @click="toRevoke = c">{{ t('invites.revoke') }}</button>
          </div>
        </li>
      </ul>
      <Pager :page="page" :pages="pages" :total="total" :busy="busy" @go="load" />
    </div>

    <ConfirmDialog
      :open="!!toRevoke"
      :title="t('invites.revokeTitle')"
      :body="t('invites.revokeBody')"
      :confirm-label="t('invites.revoke')"
      danger
      :busy="busy"
      @confirm="confirmRevoke"
      @cancel="toRevoke = null"
    />
  </section>
</template>
