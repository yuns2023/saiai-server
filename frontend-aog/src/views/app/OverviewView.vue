<script setup lang="ts">
import { computed, onMounted, ref, watch } from 'vue'
import { useI18n } from 'vue-i18n'
import ChargeTable from '@/components/ChargeTable.vue'
import Icon from '@/components/Icon.vue'
import { useAppStore } from '@/stores/app'
import { useAuthStore } from '@/stores/auth'
import { useHostedStore } from '@/stores/hosted'
import { useNoticesStore } from '@/stores/notices'
import { invitesApi, keysApi, usageApi, type DashboardStats } from '@/api'
import { intlLocale } from '@/i18n'
import { count, dateTime, money, ymd } from '@/lib/format'
import type { UsageLog, UserInviteOverview } from '@/types'

const { t } = useI18n()
const app = useAppStore()
const auth = useAuthStore()
const hosted = useHostedStore()
const notices = useNoticesStore()

const stats = ref<DashboardStats | null>(null)
const monthCost = ref<number | null>(null)
const recent = ref<UsageLog[]>([])
const keyCount = ref<number | null>(null)
const loading = ref(true)
const failed = ref(false)
const recentFailed = ref(false)

const user = computed(() => auth.user)
const latestNotices = computed(() => notices.items.slice(0, 3))

/** Hosted accounts in my name now; the shell keeps the store fresh, so this follows it. */
const hostedCurrent = computed(() => hosted.accounts.filter((a) => a.current).length)

/** Invitation status; stays null (row hidden) when the site has invites off or the request fails. */
const invites = ref<UserInviteOverview | null>(null)
/** While the answer is pending the row holds a placeholder, so the charges below jump less (it can still grow on phones or go away). */
const invitesPending = ref(false)
/** One short status; the exact reason (and every other limit) is explained on /invites. */
const inviteLine = computed(() => {
  const d = invites.value
  if (!app.settings?.user_invite_enabled || !d?.enabled) return null
  if (!d.eligible) return { text: t('overview.inviteLocked'), to: '/billing', link: t('overview.inviteTopUp') }
  const view = { to: '/invites', link: t('overview.inviteView') }
  const weekLeft = d.max_per_week > 0 ? Math.max(0, d.max_per_week - (d.created_this_week || 0)) : null
  if (d.weekly_limit_reached || weekLeft === 0) return { text: t('overview.inviteWeekDone'), ...view }
  if (!d.can_create) return { text: t('overview.inviteBlocked'), ...view }
  const text = weekLeft === null ? t('overview.inviteOpen') : t('overview.inviteWeekLeft', { n: weekLeft })
  return { text, to: '/invites', link: t('overview.inviteGo') }
})

/** Follows the public switch like the nav does; an answer that lands after it changed is dropped. */
let invitesSeq = 0
async function loadInvites(on: boolean | undefined): Promise<void> {
  const mine = ++invitesSeq
  if (!on) {
    invites.value = null
    invitesPending.value = false
    return
  }
  invitesPending.value = true
  const res = await invitesApi.overview(1, 1).catch(() => null)
  if (mine !== invitesSeq) return
  invites.value = res
  invitesPending.value = false
}
watch(() => app.settings?.user_invite_enabled, loadInvites, { immediate: true })

async function load(): Promise<void> {
  loading.value = true
  failed.value = false
  const now = new Date()
  const monthStart = new Date(now.getFullYear(), now.getMonth(), 1)
  const results = await Promise.allSettled([
    usageApi.dashboard(),
    usageApi.stats({ start_date: ymd(monthStart), end_date: ymd(now) }),
    usageApi.query({ page: 1, page_size: 8 }),
    keysApi.list(1, 1),
    auth.refreshUser()
  ])
  const [d, m, r, k] = results
  if (d.status === 'fulfilled') stats.value = d.value
  if (m.status === 'fulfilled') monthCost.value = m.value.total_actual_cost
  if (r.status === 'fulfilled') recent.value = r.value.items
  recentFailed.value = r.status === 'rejected'
  if (k.status === 'fulfilled') keyCount.value = k.value.total
  failed.value = results.slice(0, 3).every((x) => x.status === 'rejected')
  loading.value = false
}

onMounted(load)
</script>

<template>
  <section aria-labelledby="overview-title">
    <!-- Receipt header -->
    <div class="flex flex-wrap items-start justify-between gap-4">
      <h1 id="overview-title" class="text-lg font-semibold tracking-tight">{{ t('overview.title') }}</h1>
      <div class="flex gap-2">
        <RouterLink to="/billing" class="btn btn-line no-underline">{{ t('overview.topUp') }}</RouterLink>
        <RouterLink :to="{ path: '/keys', query: { new: '1' } }" class="btn btn-primary no-underline">
          <Icon name="plus" :size="16" />{{ t('overview.newKey') }}
        </RouterLink>
      </div>
    </div>

    <div class="rule-double mt-5 pb-5">
      <dl class="flex flex-wrap gap-x-10 gap-y-1 text-sm">
        <div v-if="user?.username" class="flex gap-3">
          <dt class="label self-center">{{ t('overview.holder') }}</dt>
          <dd class="truncate">{{ user.username }}</dd>
        </div>
        <div class="flex min-w-0 gap-3">
          <dt class="label self-center">{{ t('overview.account') }}</dt>
          <dd class="truncate">{{ user?.email }}</dd>
        </div>
      </dl>
      <div class="mt-5 flex flex-wrap items-end justify-between gap-x-10 gap-y-4">
        <div>
          <div class="label">{{ t('overview.balance') }}</div>
          <div class="num mt-1 text-figure font-semibold leading-none tracking-tight">{{ money(user?.balance) }}</div>
        </div>
        <dl class="grid grid-cols-3 gap-x-8 text-right">
          <div>
            <dt class="label">{{ t('overview.todaySpend') }}</dt>
            <dd class="num mt-1 text-md">
              <span v-if="loading" class="skeleton inline-block h-5 w-16 align-middle" />
              <template v-else>{{ money(stats?.today_actual_cost, 4) }}</template>
            </dd>
          </div>
          <div>
            <dt class="label">{{ t('overview.todayRequests') }}</dt>
            <dd class="num mt-1 text-md">
              <span v-if="loading" class="skeleton inline-block h-5 w-10 align-middle" />
              <template v-else>{{ count(stats?.today_requests, intlLocale()) }}</template>
            </dd>
          </div>
          <div>
            <dt class="label">{{ t('overview.monthSpend') }}</dt>
            <dd class="num mt-1 text-md">
              <span v-if="loading" class="skeleton inline-block h-5 w-16 align-middle" />
              <template v-else>{{ money(monthCost) }}</template>
            </dd>
          </div>
        </dl>
      </div>
    </div>

    <!-- Account services: lines that only exist for some accounts. -->
    <!-- Phones: one aligned label column (wrappers dissolve into the grid); wider: one inline line. -->
    <dl
      v-if="hosted.hasAny || inviteLine || invitesPending"
      class="grid grid-cols-[auto_1fr] items-baseline gap-x-4 gap-y-2 border-b border-rule-soft py-3 text-sm sm:flex sm:flex-wrap sm:gap-x-10"
      data-testid="overview-services"
    >
      <div v-if="hosted.hasAny" class="contents sm:flex sm:min-w-0 sm:flex-wrap sm:items-baseline sm:gap-x-3" data-testid="overview-hosted">
        <dt class="label">{{ t('nav.hosted') }}</dt>
        <dd class="flex flex-wrap items-baseline gap-x-3">
          <span>{{ hostedCurrent > 0 ? t('overview.hostedCount', { n: hostedCurrent }) : t('overview.hostedPast') }}</span>
          <RouterLink to="/hosted">{{ hostedCurrent > 0 ? t('overview.hostedGo') : t('overview.hostedGoPast') }}</RouterLink>
        </dd>
      </div>
      <div v-if="inviteLine || invitesPending" class="contents sm:flex sm:min-w-0 sm:flex-wrap sm:items-baseline sm:gap-x-3" data-testid="overview-invites">
        <dt class="label">{{ t('overview.invites') }}</dt>
        <dd class="flex flex-wrap items-baseline gap-x-3" :aria-busy="!inviteLine">
          <template v-if="inviteLine">
            <span>{{ inviteLine.text }}</span>
            <RouterLink :to="inviteLine.to">{{ inviteLine.link }}</RouterLink>
          </template>
          <span v-else class="skeleton inline-block h-4 w-40 align-middle" />
        </dd>
      </div>
    </dl>

    <!-- First run: no key yet -->
    <div v-if="!loading && keyCount === 0" class="border-b border-rule py-6">
      <h2 class="text-md font-semibold">{{ t('overview.firstRunTitle') }}</h2>
      <ol class="mt-3 space-y-1.5 text-sm text-ink-2">
        <li><span class="num mr-2 text-ink-3">1</span>{{ t('overview.firstRun1') }}</li>
        <li><span class="num mr-2 text-ink-3">2</span>{{ t('overview.firstRun2') }}</li>
        <li><span class="num mr-2 text-ink-3">3</span>{{ t('overview.firstRun3') }}</li>
      </ol>
      <RouterLink :to="{ path: '/keys', query: { new: '1' } }" class="btn btn-primary mt-4 no-underline">{{ t('overview.firstRunCta') }}</RouterLink>
    </div>

    <!-- Recent charges -->
    <section class="mt-9" aria-labelledby="recent-title">
      <div class="flex items-baseline justify-between">
        <h2 id="recent-title" class="text-md font-semibold">{{ t('overview.recent') }}</h2>
        <RouterLink to="/usage" class="text-sm">{{ t('overview.allUsage') }}</RouterLink>
      </div>
      <p class="mt-1 text-xs text-ink-3">{{ t('overview.recentHint') }}</p>
      <div class="mt-3">
        <p v-if="failed || recentFailed" class="py-6 text-sm text-seal" role="alert">
          {{ t('common.loadFailed') }} <button type="button" class="btn btn-quiet btn-sm" @click="load">{{ t('common.retry') }}</button>
        </p>
        <p v-else-if="!loading && !recent.length" class="border-b border-rule-soft py-8 text-sm text-ink-2">{{ t('overview.noCharges') }}</p>
        <ChargeTable v-else :rows="recent" :loading="loading" />
      </div>
    </section>

    <!-- Notices -->
    <section v-if="latestNotices.length" class="mt-10" aria-labelledby="notices-title">
      <div class="flex items-baseline justify-between">
        <h2 id="notices-title" class="text-md font-semibold">{{ t('nav.notices') }}</h2>
        <RouterLink to="/notices" class="text-sm">{{ t('common.viewAll') }}</RouterLink>
      </div>
      <ul class="mt-3 border-t border-rule-strong">
        <li v-for="n in latestNotices" :key="n.id" class="flex items-baseline gap-4 border-b border-rule-soft py-2.5 text-sm">
          <span class="num w-[9.5rem] shrink-0 text-xs text-ink-3">{{ dateTime(n.starts_at || n.created_at, intlLocale()) }}</span>
          <RouterLink :to="{ path: '/notices', hash: `#n${n.id}` }" class="min-w-0 flex-1 truncate text-ink no-underline hover:text-blue" :class="!n.read_at && 'font-medium'">
            {{ n.title }}
          </RouterLink>
          <span v-if="!n.read_at" class="tag tag-bad">{{ t('notices.new') }}</span>
        </li>
      </ul>
    </section>
  </section>
</template>
