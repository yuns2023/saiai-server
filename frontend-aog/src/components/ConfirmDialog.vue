<script setup lang="ts">
import { ref, watch } from 'vue'

const props = defineProps<{ open: boolean; title: string; body?: string; confirmLabel: string; danger?: boolean; busy?: boolean }>()
const emit = defineEmits<{ confirm: []; cancel: [] }>()
const dialog = ref<HTMLDialogElement | null>(null)

watch(
  () => props.open,
  (open) => {
    if (open && !dialog.value?.open) dialog.value?.showModal()
    if (!open && dialog.value?.open) dialog.value.close()
  }
)
</script>

<template>
  <dialog
    ref="dialog"
    class="m-auto w-[min(26rem,calc(100vw-2rem))] rounded-md border border-rule bg-paper p-0 text-ink backdrop:bg-[rgb(11_18_32/0.45)]"
    @cancel.prevent="emit('cancel')"
  >
    <div class="p-5">
      <h2 class="text-md font-semibold">{{ title }}</h2>
      <p v-if="body" class="mt-2 text-sm text-ink-2">{{ body }}</p>
      <slot />
    </div>
    <div class="flex justify-end gap-2 border-t border-rule-soft px-5 py-3">
      <button type="button" class="btn btn-line" :disabled="busy" @click="emit('cancel')">{{ $t('common.cancel') }}</button>
      <button type="button" class="btn" :class="danger ? 'btn-danger' : 'btn-primary'" :disabled="busy" @click="emit('confirm')">{{ confirmLabel }}</button>
    </div>
  </dialog>
</template>
