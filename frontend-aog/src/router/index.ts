import { createRouter, createWebHistory, type RouteRecordRaw } from 'vue-router'
import { useAuthStore } from '@/stores/auth'
import { useAppStore } from '@/stores/app'
import { i18n } from '@/i18n'
import { postLoginTarget } from '@/lib/redirect'

declare module 'vue-router' {
  interface RouteMeta {
    title?: string
    /** Signed-in pages render inside the app shell. */
    auth?: boolean
    /** Login/register: signed-in visitors are sent onward. */
    guestOnly?: boolean
  }
}

const routes: RouteRecordRaw[] = [
  { path: '/', name: 'landing', component: () => import('@/views/LandingView.vue'), meta: { title: 'nav.home' } },
  { path: '/login', name: 'login', component: () => import('@/views/auth/LoginView.vue'), meta: { title: 'auth.signIn', guestOnly: true } },
  { path: '/register', name: 'register', component: () => import('@/views/auth/RegisterView.vue'), meta: { title: 'auth.register', guestOnly: true } },
  { path: '/forgot-password', name: 'forgot', component: () => import('@/views/auth/ForgotPasswordView.vue'), meta: { title: 'auth.forgotTitle' } },
  { path: '/reset-password', name: 'reset', component: () => import('@/views/auth/ResetPasswordView.vue'), meta: { title: 'auth.resetTitle' } },
  { path: '/key-usage', name: 'key-usage', component: () => import('@/views/KeyLookupView.vue'), meta: { title: 'lookup.title' } },

  { path: '/dashboard', name: 'overview', component: () => import('@/views/app/OverviewView.vue'), meta: { title: 'nav.overview', auth: true } },
  { path: '/keys', name: 'keys', component: () => import('@/views/app/KeysView.vue'), meta: { title: 'nav.keys', auth: true } },
  { path: '/usage', name: 'usage', component: () => import('@/views/app/UsageView.vue'), meta: { title: 'nav.usage', auth: true } },
  { path: '/billing', name: 'billing', component: () => import('@/views/app/BillingView.vue'), meta: { title: 'nav.billing', auth: true } },
  { path: '/account', name: 'account', component: () => import('@/views/app/AccountView.vue'), meta: { title: 'nav.account', auth: true } },
  { path: '/notices', name: 'notices', component: () => import('@/views/app/NoticesView.vue'), meta: { title: 'nav.notices', auth: true } },

  // Paths used by the classic UI, emails and the payment provider's return URL.
  { path: '/purchase', redirect: (to) => ({ path: '/billing', query: to.query, hash: to.hash }) },
  { path: '/redeem', redirect: '/billing' },
  { path: '/subscriptions', redirect: '/billing' },
  { path: '/profile', redirect: '/account' },
  { path: '/home', redirect: '/' },
  { path: '/email-verify', redirect: (to) => ({ path: '/register', query: to.query }) },

  { path: '/:pathMatch(.*)*', name: 'not-found', component: () => import('@/views/NotFoundView.vue'), meta: { title: 'notFound.title' } }
]

export const router = createRouter({
  history: createWebHistory(),
  routes,
  scrollBehavior(_to, _from, saved) {
    return saved ?? { top: 0 }
  }
})

router.beforeEach((to) => {
  const auth = useAuthStore()
  if (to.meta.auth && !auth.isAuthenticated) {
    return { path: '/login', query: { redirect: to.fullPath } }
  }
  if (to.meta.guestOnly && auth.isAuthenticated) {
    const target = postLoginTarget(to.query.redirect, auth.isAdmin)
    if (target.fullPage) {
      window.location.assign(target.path)
      return false
    }
    return target.path
  }
  return true
})

router.afterEach((to) => {
  const app = useAppStore()
  const key = to.meta.title
  const label = key ? i18n.global.t(key) : ''
  document.title = label && to.name !== 'landing' ? `${label} · ${app.siteName}` : app.siteName
})
