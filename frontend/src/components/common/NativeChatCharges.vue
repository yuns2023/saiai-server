<template>
  <div v-if="isNativeChatUsage(record)" class="text-xs text-gray-500 dark:text-gray-400" data-testid="native-chat-charges">
    {{ t('usage.nativeChatTurnFee') }} ${{ costs.turn.toFixed(6) }} +
    {{ t('usage.nativeChatImageFee') }} ${{ costs.image.toFixed(6) }}
  </div>
</template>

<script setup lang="ts">
import { computed } from 'vue'
import { useI18n } from 'vue-i18n'
import { isNativeChatUsage, nativeChatChargeCosts } from '@/utils/nativeChatUsage'

const props = defineProps<{ record: Parameters<typeof nativeChatChargeCosts>[0] }>()
const { t } = useI18n()
const costs = computed(() => nativeChatChargeCosts(props.record))
</script>
