<script setup lang="ts">
import { computed, onMounted, ref, watch } from 'vue'
import { useRoute, useRouter } from 'vue-router'
import { useI18n } from 'vue-i18n'
import { useAppStore } from '@/stores/app'
import { useAuthStore } from '@/stores/auth'
import { useNoticesStore } from '@/stores/notices'
import Icon from './Icon.vue'
import PrefsMenu from './PrefsMenu.vue'

const { t } = useI18n()
const app = useAppStore()
const auth = useAuthStore()
const notices = useNoticesStore()
const route = useRoute()
const router = useRouter()
const drawerOpen = ref(false)

const nav = computed(() => [
  { to: '/dashboard', icon: 'overview' as const, label: t('nav.overview') },
  { to: '/keys', icon: 'key' as const, label: t('nav.keys') },
  { to: '/usage', icon: 'usage' as const, label: t('nav.usage') },
  { to: '/billing', icon: 'billing' as const, label: t('nav.billing') },
  { to: '/account', icon: 'account' as const, label: t('nav.account') },
  { to: '/notices', icon: 'bell' as const, label: t('nav.notices'), count: notices.unread.length }
])

watch(() => route.fullPath, () => (drawerOpen.value = false))

onMounted(() => {
  if (!notices.loaded) notices.load().catch(() => undefined)
})

async function signOut(): Promise<void> {
  await auth.logout()
  router.replace('/login')
}
</script>

<template>
  <div class="min-h-dvh bg-paper lg:grid lg:grid-cols-[13rem_1fr]">
    <!-- Mobile bar -->
    <header class="sticky top-0 z-30 flex h-12 items-center justify-between border-b border-rule bg-shell px-4 lg:hidden">
      <RouterLink to="/dashboard" class="font-semibold text-ink no-underline">{{ app.siteName }}</RouterLink>
      <button type="button" class="btn btn-quiet btn-sm text-ink" :aria-expanded="drawerOpen" :aria-label="t('nav.menu')" @click="drawerOpen = !drawerOpen">
        <Icon :name="drawerOpen ? 'close' : 'menu'" />
      </button>
    </header>

    <div v-if="drawerOpen" class="fixed inset-0 z-30 bg-[rgb(11_18_32/0.35)] lg:hidden" @click="drawerOpen = false" />

    <!-- Rail -->
    <aside
      class="fixed inset-y-0 left-0 z-40 flex w-52 flex-col border-r border-rule bg-shell transition-transform duration-200 ease-out lg:sticky lg:top-0 lg:h-dvh lg:translate-x-0"
      :class="drawerOpen ? 'translate-x-0' : '-translate-x-full'"
    >
      <div class="px-5 pb-4 pt-5">
        <RouterLink to="/dashboard" class="flex items-center gap-2 text-ink no-underline">
          <img v-if="app.siteLogo" :src="app.siteLogo" alt="" class="h-6 w-6 rounded-sm object-contain" />
          <span class="text-md font-semibold tracking-tight">{{ app.siteName }}</span>
        </RouterLink>
      </div>

      <nav class="flex-1 overflow-y-auto px-3" :aria-label="t('nav.main')">
        <ul class="space-y-0.5">
          <li v-for="item in nav" :key="item.to">
            <RouterLink
              :to="item.to"
              class="flex h-9 items-center gap-2.5 rounded px-2.5 text-sm text-ink-2 no-underline transition-colors duration-150 hover:bg-rule-soft hover:text-ink"
              active-class="!bg-blue-wash !text-blue font-medium"
            >
              <Icon :name="item.icon" :size="17" />
              <span class="flex-1">{{ item.label }}</span>
              <span v-if="item.count" class="num rounded-sm bg-seal px-1.5 text-2xs leading-4 text-white" :aria-label="t('notices.unreadCount', { n: item.count })">{{ item.count }}</span>
            </RouterLink>
          </li>
        </ul>
        <a
          v-if="auth.isAdmin"
          href="/admin/dashboard"
          class="mt-4 flex h-9 items-center gap-2.5 rounded border border-dashed border-rule px-2.5 text-sm text-ink-2 no-underline hover:border-ink-3 hover:text-ink"
        >
          <Icon name="console" :size="17" />
          <span>{{ t('nav.admin') }}</span>
        </a>
      </nav>

      <div class="border-t border-rule px-3 py-3">
        <PrefsMenu compact />
        <div class="mt-2 flex items-center justify-between gap-2 px-2">
          <span class="min-w-0 truncate text-xs text-ink-3" :title="auth.user?.email">{{ auth.user?.email }}</span>
          <button type="button" class="btn btn-quiet btn-sm text-ink-2" :title="t('nav.signOut')" :aria-label="t('nav.signOut')" @click="signOut">
            <Icon name="logout" :size="15" />
          </button>
        </div>
      </div>
    </aside>

    <!-- Content -->
    <main class="min-w-0">
      <div
        v-if="notices.banner"
        class="flex items-center gap-3 border-b border-rule bg-blue-wash px-5 py-2 text-sm text-ink lg:px-10"
        role="note"
      >
        <Icon name="bell" :size="15" class="text-blue" />
        <span class="min-w-0 flex-1 truncate">{{ notices.banner.title }}</span>
        <RouterLink :to="{ path: '/notices', hash: `#n${notices.banner.id}` }" class="shrink-0">{{ t('notices.read') }}</RouterLink>
        <button type="button" class="shrink-0 text-ink-3 hover:text-ink" :aria-label="t('common.dismiss')" @click="notices.markRead(notices.banner!.id)">
          <Icon name="close" :size="14" />
        </button>
      </div>
      <div class="mx-auto w-full max-w-[72rem] px-5 pb-16 pt-6 lg:px-10 lg:pt-9">
        <slot />
      </div>
    </main>
  </div>
</template>
