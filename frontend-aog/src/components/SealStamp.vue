<script setup lang="ts">
/**
 * The "credited" seal. Stamps once when a redeem or payment lands.
 * Drawn in CSS/SVG text, seal red, slight rotation like a hand stamp.
 */
defineProps<{ text: string; sub?: string }>()
</script>

<template>
  <div class="seal" role="img" :aria-label="text">
    <svg viewBox="0 0 120 120" width="96" height="96" aria-hidden="true">
      <circle cx="60" cy="60" r="55" fill="none" stroke="currentColor" stroke-width="3.5" />
      <circle cx="60" cy="60" r="47" fill="none" stroke="currentColor" stroke-width="1.2" />
      <text x="60" y="66" text-anchor="middle" font-size="22" font-weight="700" fill="currentColor" letter-spacing="2">{{ text }}</text>
      <text v-if="sub" x="60" y="88" text-anchor="middle" font-size="9.5" fill="currentColor" class="num">{{ sub }}</text>
    </svg>
  </div>
</template>

<style scoped>
.seal {
  color: var(--seal);
  transform: rotate(-9deg);
  animation: stamp 220ms cubic-bezier(0.16, 1, 0.3, 1) both;
  opacity: 0.92;
}
@keyframes stamp {
  from {
    transform: rotate(-9deg) scale(1.35);
    opacity: 0;
  }
  to {
    transform: rotate(-9deg) scale(1);
    opacity: 0.92;
  }
}
</style>
