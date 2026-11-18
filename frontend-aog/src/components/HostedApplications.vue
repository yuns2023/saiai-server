<script setup lang="ts">
/** 我的托管申请: my self-onboarding applications, newest first; a pending one can be cancelled. */
import { computed, ref } from 'vue'
import { useI18n } from 'vue-i18n'
import { hostedApi, type HostedApplication } from '@/api'
import { intlLocale } from '@/i18n'
import { dateTime, shortTime } from '@/lib/format'
import { onboardErrorKey } from '@/lib/selfOnboard'
import { useAppStore } from '@/stores/app'
import ConfirmDialog from './ConfirmDialog.vue'

const props = defineProps<{ items: HostedApplication[] }>()
const emit = defineEmits<{ changed: [app: HostedApplication] }>()
const { t, te } = useI18n()
const app = useAppStore()

const sorted = computed(() => [...props.items].sort((a, b) => Date.parse(b.created_at) - Date.parse(a.created_at) || b.id - a.id))

function statusLabel(s: string): string {
  return te(`onboard.apps.status_${s}`) ? t(`onboard.apps.status_${s}`) : s
}

function statusTag(s: string): string {
  if (s === 'pending') return 'tag-warn'
  if (s === 'approved') return 'tag-ok'
  if (s === 'rejected') return 'tag-bad'
  return 'tag-off'
}

const target = ref<HostedApplication | null>(null)
const busy = ref(false)

async function confirmCancel(): Promise<void> {
  const a = target.value
  if (!a || busy.value) return
  busy.value = true
  try {
    const updated = await hostedApi.cancelApplication(a.id)
    emit('changed', updated)
    app.notify('ok', t('onboard.apps.cancelled'))
  } catch (e) {
    app.notify('error', t(`onboard.errors.${onboardErrorKey(e)}`))
    // NOT_FOUND / INVALID_STATE: it was handled meanwhile; the parent reloads the list.
    emit('changed', a)
  } finally {
    busy.value = false
    target.value = null
  }
}
</script>

<template>
  <section class="mt-12" aria-labelledby="apps-title" data-testid="apps">
    <h2 id="apps-title" class="text-md font-semibold">{{ t('onboard.apps.title') }}</h2>
    <ul class="mt-3 border-t border-rule-strong">
      <li
        v-for="a in sorted"
        :key="a.id"
        class="flex flex-wrap items-start gap-x-6 gap-y-2 border-b border-rule-soft px-3 py-4"
        :data-testid="`app-${a.id}`"
      >
        <div class="min-w-0 flex-1">
          <div class="flex flex-wrap items-center gap-x-2 gap-y-1">
            <span class="min-w-0 truncate font-medium">{{ a.display_name }}</span>
            <span class="tag" :class="statusTag(a.status)" data-testid="app-status">{{ statusLabel(a.status) }}</span>
          </div>
          <div class="mt-1 flex flex-wrap gap-x-4 gap-y-1 text-xs text-ink-3">
            <span v-if="a.upstream_email" class="min-w-0 truncate">{{ a.upstream_email }}</span>
            <span class="min-w-0 truncate" data-testid="app-proxy">
              {{ t('onboard.apps.proxy') }}: <span class="font-mono">{{ a.proxy_display || t('onboard.apps.proxyNone') }}</span>
            </span>
            <span :title="dateTime(a.created_at, intlLocale())">{{ t('onboard.apps.submittedAt', { time: shortTime(a.created_at, intlLocale()) }) }}</span>
            <span v-if="a.reviewed_at" :title="dateTime(a.reviewed_at, intlLocale())">{{ t('onboard.apps.reviewedAt', { time: shortTime(a.reviewed_at, intlLocale()) }) }}</span>
          </div>
          <p v-if="a.status === 'rejected' && a.reject_reason" class="mt-2 max-w-[70ch] text-sm text-ink" data-testid="app-reason">
            {{ t('onboard.apps.rejectReason', { reason: a.reject_reason }) }}
          </p>
          <p v-else-if="a.status === 'approved'" class="mt-2 text-sm text-ink-2">{{ t('onboard.apps.approvedNote') }}</p>
          <p v-else-if="a.status === 'pending'" class="mt-2 text-sm text-ink-2">{{ t('onboard.apps.pendingNote') }}</p>
          <p v-else-if="a.status === 'expired'" class="mt-2 text-sm text-ink-2">{{ t('onboard.apps.expiredNote') }}</p>
        </div>
        <button
          v-if="a.status === 'pending'"
          type="button"
          class="btn btn-line btn-sm"
          :disabled="busy"
          :data-testid="`app-cancel-${a.id}`"
          @click="target = a"
        >
          {{ t('onboard.apps.cancel') }}
        </button>
      </li>
    </ul>

    <ConfirmDialog
      :open="!!target"
      :title="t('onboard.apps.cancelTitle')"
      :body="t('onboard.apps.cancelBody')"
      :confirm-label="t('onboard.apps.cancelConfirm')"
      danger
      :busy="busy"
      @confirm="confirmCancel"
      @cancel="target = null"
    />
  </section>
</template>
