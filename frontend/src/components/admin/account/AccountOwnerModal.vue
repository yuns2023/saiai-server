<template>
  <BaseDialog :show="show" :title="t('admin.accounts.owner.title')" :close-on-escape="!busy" @close="close">
    <p class="mb-2 text-sm text-gray-700 dark:text-gray-300" data-testid="owner-current">
      <template v-if="account?.owner_user_id">
        {{ t('admin.accounts.owner.current', { owner: currentOwnerLabel }) }}
      </template>
      <template v-else>{{ t('admin.accounts.owner.none') }}</template>
    </p>
    <p class="mb-4 text-xs text-gray-500 dark:text-gray-400" data-testid="owner-rule">{{ t('admin.accounts.owner.rule') }}</p>
    <p v-if="error" role="alert" class="mb-3 text-sm text-red-600" data-testid="owner-error">{{ error }}</p>
    <label class="input-label">{{ t('admin.accounts.owner.pickLabel') }}</label>
    <CarpoolUserSelector v-model="selected" single />
    <template #footer>
      <button class="btn btn-secondary" :disabled="busy" @click="close">{{ t('common.cancel') }}</button>
      <button v-if="account?.owner_user_id" class="btn btn-secondary" :disabled="busy" data-testid="owner-clear" @click="submit(null)">
        {{ t('admin.accounts.owner.clear') }}
      </button>
      <button class="btn btn-primary" :disabled="busy || !canSave" data-testid="owner-save" @click="submit(selected[0])">
        {{ t('common.save') }}
      </button>
    </template>
  </BaseDialog>
</template>

<script setup lang="ts">
import { computed, ref, watch } from 'vue'
import { useI18n } from 'vue-i18n'
import BaseDialog from '@/components/common/BaseDialog.vue'
import CarpoolUserSelector from '@/components/account/CarpoolUserSelector.vue'
import { accountsAPI, ACCOUNT_OWNER_TRANSFER_FORBIDDEN, type AccountOwnerResult } from '@/api/admin/accounts'
import { useAppStore } from '@/stores/app'
import type { Account } from '@/types'

const props = defineProps<{ show: boolean; account: Account | null }>()
const emit = defineEmits<{ close: []; updated: [result: AccountOwnerResult] }>()
const { t } = useI18n()
const appStore = useAppStore()

const selected = ref<number[]>([])
const busy = ref(false)
const error = ref('')

const currentOwnerLabel = computed(() => {
  const account = props.account
  if (!account?.owner_user_id) return ''
  return account.owner_email ? `${account.owner_email} (#${account.owner_user_id})` : `#${account.owner_user_id}`
})
const canSave = computed(() => selected.value.length === 1 && selected.value[0] !== props.account?.owner_user_id)

const close = () => { if (!busy.value) emit('close') }

const errorText = (value: unknown) => {
  const err = value as { reason?: string; message?: string } | null
  if (err?.reason === ACCOUNT_OWNER_TRANSFER_FORBIDDEN) return t('admin.accounts.owner.transferForbidden')
  return err?.message || t('admin.accounts.owner.failed')
}

async function submit(ownerUserId: number | null) {
  const account = props.account
  if (!account || busy.value) return
  busy.value = true
  error.value = ''
  try {
    const result = await accountsAPI.setAccountOwner(account.id, ownerUserId)
    appStore.showSuccess(t(ownerUserId === null ? 'admin.accounts.owner.cleared' : 'admin.accounts.owner.saved'))
    emit('updated', result)
    emit('close')
  } catch (value) {
    error.value = errorText(value)
  } finally {
    busy.value = false
  }
}

watch(() => [props.show, props.account?.id] as const, ([show]) => {
  if (!show) return
  selected.value = props.account?.owner_user_id ? [props.account.owner_user_id] : []
  error.value = ''
}, { immediate: true })
</script>
