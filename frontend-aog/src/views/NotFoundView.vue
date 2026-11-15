<script setup lang="ts">
import { useI18n } from 'vue-i18n'
import AuthFrame from '@/components/AuthFrame.vue'
const { t } = useI18n()
</script>

<template>
  <AuthFrame :title="t('notFound.title')" :lead="t('notFound.lead')">
    <RouterLink to="/" class="btn btn-line no-underline">{{ t('notFound.home') }}</RouterLink>
  </AuthFrame>
</template>
