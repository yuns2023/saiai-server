<script setup lang="ts">
import { computed, nextTick, ref, useId, useTemplateRef } from 'vue'
import { useEventListener } from '@vueuse/core'
import { useI18n } from 'vue-i18n'
import { formatExactTokens, formatTokenCount, type TokenBreakdown } from '@/utils/usageTokens'

defineProps<{
  value?: number | null
  label?: string
  breakdown?: TokenBreakdown
}>()

const { t } = useI18n()
const id = useId()
const trigger = useTemplateRef<HTMLElement>('trigger')
const panel = useTemplateRef<HTMLElement>('panel')
const open = ref(false)
const position = ref({ top: '0px', left: '0px' })
const hide = () => { open.value = false }
const show = async () => {
  open.value = true
  await nextTick()
  if (!trigger.value || !panel.value) return
  const rect = trigger.value.getBoundingClientRect()
  const bounds = panel.value.getBoundingClientRect()
  position.value = {
    left: `${Math.max(8, Math.min(rect.left, window.innerWidth - bounds.width - 8))}px`,
    top: `${rect.bottom + bounds.height + 8 < window.innerHeight ? rect.bottom + 8 : Math.max(8, rect.top - bounds.height - 8)}px`
  }
}
const activeWindow = computed(() => open.value ? window : undefined)
useEventListener(activeWindow, 'pointerdown', (event) => {
  const target = event.target as Node | null
  if (target && !trigger.value?.contains(target) && !panel.value?.contains(target)) hide()
})
useEventListener(activeWindow, 'scroll', hide, true)
useEventListener(activeWindow, 'resize', hide)
</script>

<template>
  <span class="inline-flex tabular-nums">
    <button
      ref="trigger"
      type="button"
      class="cursor-help rounded-sm text-left focus-visible:outline focus-visible:outline-2 focus-visible:outline-primary-500"
      :aria-label="`${label || t('tokens.total')}: ${formatExactTokens(value)}`"
      :aria-describedby="open ? id : undefined"
      @mouseenter="show"
      @mouseleave="hide"
      @focus="show"
      @blur="hide"
      @click.stop="show"
      @keydown.esc.stop="hide"
    >{{ formatTokenCount(value) }}</button>
    <Teleport to="body">
      <div
        v-if="open"
        :id="id"
        ref="panel"
        role="tooltip"
        class="fixed z-99999 max-w-[calc(100vw-16px)] rounded-lg bg-gray-900 p-3 text-xs font-normal leading-relaxed text-white shadow-xl"
        :style="position"
      >
        <p class="font-semibold">{{ label || t('tokens.total') }}: {{ formatExactTokens(value) }}</p>
        <template v-if="breakdown">
          <dl class="mt-2 grid grid-cols-[auto_auto] gap-x-6 gap-y-1 tabular-nums">
            <dt class="text-emerald-300">{{ t('tokens.input') }}</dt><dd class="text-right">{{ formatExactTokens(breakdown.input) }}</dd>
            <dt class="text-violet-300">{{ t('tokens.output') }}</dt><dd class="text-right">{{ formatExactTokens(breakdown.output) }}</dd>
            <dt class="text-sky-300">{{ t('tokens.cacheRead') }}</dt><dd class="text-right">{{ formatExactTokens(breakdown.cacheRead) }}</dd>
            <dt class="text-amber-300">{{ t('tokens.cacheWrite') }}</dt><dd class="text-right">{{ formatExactTokens(breakdown.cacheWrite) }}</dd>
          </dl>
          <p class="mt-2 max-w-72 border-t border-white/20 pt-2 text-gray-300">{{ t('tokens.formula') }}</p>
          <p class="max-w-72 text-gray-400">{{ t('tokens.roundingHint') }}</p>
        </template>
      </div>
    </Teleport>
  </span>
</template>
