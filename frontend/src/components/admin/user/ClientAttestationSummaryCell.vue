<template>
  <span v-if="summary.kind === 'none'" class="text-sm text-gray-400" :title="hint">-</span>
  <span
    v-else-if="summary.kind === 'all_valid'"
    :class="['inline-flex items-center rounded px-1.5 py-0.5 text-xs font-medium', clientAttestationBadgeClass('valid')]"
    :title="`${hint}\n${t('admin.users.clientProof.status.valid')}: ${summary.count}`"
    data-testid="client-attestation-all-valid"
  >
    {{ t('admin.users.clientProof.allValid') }}
  </span>
  <span v-else class="flex flex-wrap gap-1" :title="mixedTitle" data-testid="client-attestation-mixed">
    <span
      v-for="entry in summary.entries"
      :key="entry.status"
      :class="['inline-flex items-center rounded px-1.5 py-0.5 text-xs font-medium', clientAttestationBadgeClass(entry.status)]"
    >
      {{ t(`admin.users.clientProof.status.${entry.status}`) }} {{ entry.count }}
    </span>
  </span>
</template>

<script setup lang="ts">
import { computed } from 'vue'
import { useI18n } from 'vue-i18n'
import type { ClientAttestationStatus } from '@/types'
import { clientAttestationBadgeClass, summarizeClientAttestation } from '@/utils/clientAttestation'

const props = defineProps<{ counts?: Partial<Record<ClientAttestationStatus, number>> | null }>()
const { t } = useI18n()

const hint = computed(() => (props.counts === null || props.counts === undefined
  ? `${t('admin.users.clientProof.attestationHint')}\n${t('admin.users.clientProof.unavailable')}`
  : t('admin.users.clientProof.attestationHint')))
const summary = computed(() => summarizeClientAttestation(props.counts))
const mixedTitle = computed(() => {
  if (summary.value.kind !== 'mixed') return hint.value
  return [hint.value, ...summary.value.entries.map((entry) => `${t(`admin.users.clientProof.status.${entry.status}`)}: ${entry.count}`)].join('\n')
})
</script>
