<script setup lang="ts">
const siteName = window.__APP_CONFIG__?.site_name?.trim() || 'AOGCode'
</script>

<template>
  <main style="font-family: system-ui, sans-serif; max-width: 32rem; margin: 18vh auto; padding: 0 1.5rem; color: #14203a">
    <h1 style="font-size: 1.5rem; font-weight: 600">{{ siteName }}</h1>
    <p style="color: #47536e">The customer console is being prepared.</p>
  </main>
</template>
