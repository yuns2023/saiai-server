<script setup lang="ts">
// Hosted self-onboarding markers on the account list. Every field is optional
// (older servers send none of them); a badge shows only when its field is set.
import { useI18n } from 'vue-i18n'
import type { Account } from '@/types'

defineProps<{ account: Pick<Account, 'self_onboarded' | 'identity_unknown' | 'owner_paused_at' | 'owner_withdraw_requested_at'> }>()
const { t } = useI18n()
</script>

<template>
  <span
    v-if="account.self_onboarded || account.identity_unknown || account.owner_paused_at || account.owner_withdraw_requested_at"
    class="mt-0.5 flex flex-wrap gap-1"
  >
    <span v-if="account.self_onboarded" class="badge badge-primary" data-testid="account-self-onboarded">{{ t('admin.accounts.selfOnboarded') }}</span>
    <span v-if="account.identity_unknown" class="badge badge-gray" :title="t('admin.accounts.identityUnknownHint')" data-testid="account-identity-unknown">{{ t('admin.accounts.identityUnknown') }}</span>
    <span v-if="account.owner_paused_at" class="badge badge-warning" :title="account.owner_paused_at" data-testid="account-owner-paused">{{ t('admin.accounts.ownerPaused') }}</span>
    <span v-if="account.owner_withdraw_requested_at" class="badge badge-danger" :title="account.owner_withdraw_requested_at" data-testid="account-withdraw-requested">{{ t('admin.accounts.withdrawRequested') }}</span>
  </span>
</template>
