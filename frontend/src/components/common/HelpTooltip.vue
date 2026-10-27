<script setup lang="ts">
import { computed, ref, useId, useTemplateRef, nextTick, onMounted, onBeforeUnmount, watch } from 'vue'

const props = withDefaults(defineProps<{
  content?: string
  /**
   * Only mount the tooltip body while it is open. Use for many per-row tooltips
   * whose trigger already carries the full text as its accessible name.
   */
  lazy?: boolean
  /** Tap/click pins the tooltip open. Disable when the trigger itself acts on click (e.g. opens a dialog). */
  pinOnClick?: boolean
}>(), { lazy: false, pinOnClick: true })

const tooltipId = `help-tooltip-${useId()}`
const hovered = ref(false)
const focused = ref(false)
// Tap/click pins the tooltip open (touch devices have no hover and iOS does not focus on tap).
const pinned = ref(false)
const show = computed(() => hovered.value || focused.value || pinned.value)
const triggerRef = useTemplateRef<HTMLElement>('trigger')
const tooltipRef = useTemplateRef<HTMLElement>('tooltip')
const placement = ref<'top' | 'bottom'>('top')
const tooltipStyle = ref({ top: '0px', left: '0px' })
const arrowOffset = ref(0)

const GAP = 8
const HALF_WIDTH = 128 // w-64
const VIEWPORT_MARGIN = 8

// Hover, keyboard focus and tap are tracked separately so one cannot hide the other.
function open(source: typeof hovered) {
  source.value = true
  nextTick(updatePosition)
}

function onEnter() {
  open(hovered)
}

function onLeave() {
  hovered.value = false
}

function onFocusIn() {
  open(focused)
}

function onFocusOut() {
  focused.value = false
}

function closeAll() {
  hovered.value = false
  focused.value = false
  pinned.value = false
}

function onClick() {
  if (!props.pinOnClick) {
    closeAll()
    return
  }
  if (pinned.value) {
    closeAll()
    return
  }
  open(pinned)
}

function onOutsidePointer(event: Event) {
  const target = event.target as Node
  if (triggerRef.value?.contains(target) || tooltipRef.value?.contains(target)) return
  closeAll()
}

function onKeydown(event: Event) {
  if ((event as KeyboardEvent).key !== 'Escape') return
  // Close this layer first (e.g. before an enclosing dialog reacts).
  event.stopPropagation()
  closeAll()
}

// A fixed-position tooltip would drift away from its trigger on scroll/resize.
function onViewportChange() {
  closeAll()
}

function setGlobalListeners(active: boolean) {
  const method = active ? 'addEventListener' : 'removeEventListener'
  document[method]('pointerdown', onOutsidePointer, true)
  document[method]('keydown', onKeydown, true)
  window[method]('scroll', onViewportChange, true)
  window[method]('resize', onViewportChange)
}

// Content can change while open (e.g. loaded asynchronously); keep it placed inside the viewport.
let resizeObserver: ResizeObserver | null = null
function observeSize(active: boolean) {
  resizeObserver?.disconnect()
  resizeObserver = null
  if (!active || typeof ResizeObserver === 'undefined') return
  nextTick(() => {
    if (resizeObserver || !tooltipRef.value || !show.value) return
    resizeObserver = new ResizeObserver(() => updatePosition())
    resizeObserver.observe(tooltipRef.value)
  })
}

watch(show, (visible) => {
  setGlobalListeners(visible)
  observeSize(visible)
})
onBeforeUnmount(() => {
  setGlobalListeners(false)
  observeSize(false)
})

// Describe the focusable trigger by the tooltip body (WAI-ARIA tooltip pattern).
onMounted(() => {
  const focusable = triggerRef.value?.querySelector<HTMLElement>('button, a[href], input, [tabindex]')
  if (focusable && !focusable.hasAttribute('aria-describedby')) focusable.setAttribute('aria-describedby', tooltipId)
})

function updatePosition() {
  const el = triggerRef.value
  if (!el) return
  // The tooltip is position: fixed, so viewport coordinates are used directly.
  const rect = el.getBoundingClientRect()
  const height = tooltipRef.value?.offsetHeight ?? 0
  // Prefer above; flip below when it would be clipped by the top of the viewport.
  placement.value = rect.top - GAP - height < VIEWPORT_MARGIN ? 'bottom' : 'top'
  const center = rect.left + rect.width / 2
  const minCenter = HALF_WIDTH + VIEWPORT_MARGIN
  const maxCenter = Math.max(minCenter, window.innerWidth - HALF_WIDTH - VIEWPORT_MARGIN)
  const clamped = Math.min(Math.max(center, minCenter), maxCenter)
  arrowOffset.value = center - clamped
  tooltipStyle.value = {
    top: placement.value === 'top' ? `${rect.top - GAP}px` : `${rect.bottom + GAP}px`,
    left: `${clamped}px`,
  }
}

const rendered = computed(() => !props.lazy || show.value)
</script>

<template>
  <span
    ref="trigger"
    class="group relative ml-1 inline-flex items-center align-middle"
    @mouseenter="onEnter"
    @mouseleave="onLeave"
    @focusin="onFocusIn"
    @focusout="onFocusOut"
    @click="onClick"
  >
    <!-- Trigger Icon -->
    <slot name="trigger">
      <svg
        class="h-4 w-4 cursor-help text-gray-400 transition-colors hover:text-primary-600 dark:text-gray-500 dark:hover:text-primary-400"
        fill="none"
        viewBox="0 0 24 24"
        stroke="currentColor"
        stroke-width="2"
      >
        <path
          stroke-linecap="round"
          stroke-linejoin="round"
          d="M13 16h-1v-4h-1m1-4h.01M21 12a9 9 0 11-18 0 9 9 0 0118 0z"
        />
      </svg>
    </slot>

    <!-- Teleport to body to escape modal overflow clipping -->
    <Teleport to="body">
      <div
        v-if="rendered"
        v-show="show"
        :id="tooltipId"
        ref="tooltip"
        role="tooltip"
        class="fixed z-99999 w-64 -translate-x-1/2 rounded-lg bg-gray-900 p-3 text-xs leading-relaxed text-white shadow-xl ring-1 ring-white/10 dark:bg-gray-800"
        :class="placement === 'top' ? '-translate-y-full' : ''"
        :style="tooltipStyle"
      >
        <slot>{{ content }}</slot>
        <div
          class="absolute h-2 w-2 -translate-x-1/2 rotate-45 bg-gray-900 dark:bg-gray-800"
          :class="placement === 'top' ? '-bottom-1' : '-top-1'"
          :style="{ left: `calc(50% + ${arrowOffset}px)` }"
        ></div>
      </div>
    </Teleport>
  </span>
</template>
