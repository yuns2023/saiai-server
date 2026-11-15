<script setup lang="ts">
import { useAppStore } from '@/stores/app'
import PrefsMenu from './PrefsMenu.vue'

defineProps<{ title: string; lead?: string }>()
const app = useAppStore()
</script>

<template>
  <div class="flex min-h-dvh flex-col bg-paper">
    <header class="flex h-14 items-center justify-between px-5 sm:px-8">
      <RouterLink to="/" class="flex items-center gap-2 text-ink no-underline">
        <img v-if="app.siteLogo" :src="app.siteLogo" alt="" class="h-6 w-6 rounded-sm object-contain" />
        <span class="text-md font-semibold tracking-tight">{{ app.siteName }}</span>
      </RouterLink>
      <PrefsMenu compact />
    </header>
    <main class="flex flex-1 items-start justify-center px-5 pb-16 pt-[min(12vh,7rem)]">
      <div class="w-full max-w-[22rem]">
        <h1 class="rule-double pb-3 text-xl font-semibold tracking-tight">{{ title }}</h1>
        <p v-if="lead" class="mt-3 text-sm text-ink-2">{{ lead }}</p>
        <div class="mt-6">
          <p v-if="app.settingsFailed" class="text-sm text-seal" role="alert">
            {{ $t('auth.settingsFailed') }}
            <button type="button" class="btn btn-quiet btn-sm" @click="app.loadSettings(true)">{{ $t('common.retry') }}</button>
          </p>
          <slot v-else />
        </div>
      </div>
    </main>
  </div>
</template>
