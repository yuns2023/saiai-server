<template>
  <BaseDialog :show="show" :title="t('keys.moreActions')" @close="emit('close')">
    <div v-if="show" class="space-y-4">
      <p class="font-medium text-gray-900 dark:text-white">{{ keyName }}</p>
      <p class="text-sm text-gray-600 dark:text-gray-400">{{ t('keys.rawKeyHint') }}</p>
      <div class="rounded-xl bg-gray-100 p-4 dark:bg-dark-700">
        <p class="mb-2 text-sm font-medium">{{ t('keys.apiKey') }}</p>
        <code class="block break-all font-mono text-sm">{{ revealed ? apiKey : '••••••••••••••••' }}</code>
      </div>
      <div class="flex flex-wrap gap-2">
        <button type="button" class="btn btn-secondary" :aria-pressed="revealed" @click="revealed = !revealed">
          <Icon :name="revealed ? 'eyeOff' : 'eye'" size="sm" />
          {{ t(revealed ? 'keys.hideKey' : 'keys.showKey') }}
        </button>
        <button type="button" class="btn btn-secondary" @click="copyKey">
          <Icon :name="copied ? 'check' : 'clipboard'" size="sm" />
          {{ t(copied ? 'keys.copied' : 'keys.copyKey') }}
        </button>
      </div>
      <p class="text-xs text-gray-500 dark:text-gray-400">{{ t('keys.rawKeyWarning') }}</p>
    </div>
    <template #footer>
      <button type="button" class="btn btn-secondary" @click="emit('close')">{{ t('common.close') }}</button>
    </template>
  </BaseDialog>
</template>

<script setup lang="ts">
import { ref, watch } from 'vue'
import { useI18n } from 'vue-i18n'
import BaseDialog from '@/components/common/BaseDialog.vue'
import Icon from '@/components/icons/Icon.vue'
import { useClipboard } from '@/composables/useClipboard'

const props = defineProps<{ show: boolean; apiKey: string; keyName: string }>()
const emit = defineEmits<{ (event: 'close'): void }>()
const { t } = useI18n()
const { copyToClipboard } = useClipboard()
const revealed = ref(false)
const copied = ref(false)

watch(() => [props.show, props.apiKey], () => {
  revealed.value = false
  copied.value = false
})

async function copyKey() {
  const key = props.apiKey
  const success = await copyToClipboard(key, t('keys.copied'))
  if (props.show && props.apiKey === key) copied.value = success
}
</script>
