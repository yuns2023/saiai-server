<template>
  <div class="card">
    <div class="border-b border-gray-100 px-6 py-4 dark:border-dark-700">
      <h2 class="text-lg font-semibold text-gray-900 dark:text-white">
        {{ t('admin.settings.socialOAuth.title', { provider: label }) }}
      </h2>
      <p class="mt-1 text-sm text-gray-500 dark:text-gray-400">
        {{ t('admin.settings.socialOAuth.description', { provider: label }) }}
      </p>
    </div>
    <div class="space-y-5 p-6">
      <div class="flex items-center justify-between">
        <div>
          <label class="font-medium text-gray-900 dark:text-white">{{
            t('admin.settings.socialOAuth.enable', { provider: label })
          }}</label>
          <p class="text-sm text-gray-500 dark:text-gray-400">
            {{ t('admin.settings.socialOAuth.enableHint') }}
          </p>
        </div>
        <Toggle v-model="enabled" />
      </div>

      <div v-if="enabled" class="border-t border-gray-100 pt-4 dark:border-dark-700">
        <div class="grid grid-cols-1 gap-6">
          <div>
            <label class="mb-2 block text-sm font-medium text-gray-700 dark:text-gray-300">Client ID</label>
            <input v-model="clientId" type="text" class="input font-mono text-sm" autocomplete="off" />
          </div>

          <div>
            <label class="mb-2 block text-sm font-medium text-gray-700 dark:text-gray-300">Client Secret</label>
            <input
              v-model="clientSecret"
              type="password"
              class="input font-mono text-sm"
              autocomplete="new-password"
              placeholder="********"
            />
            <p class="mt-1.5 text-xs text-gray-500 dark:text-gray-400">
              {{
                secretConfigured
                  ? t('admin.settings.socialOAuth.secretConfiguredHint')
                  : t('admin.settings.socialOAuth.secretHint')
              }}
            </p>
          </div>

          <div>
            <label class="mb-2 block text-sm font-medium text-gray-700 dark:text-gray-300">
              {{ t('admin.settings.socialOAuth.redirectUrl') }}
            </label>
            <code
              v-if="redirectUrl"
              class="block select-all break-all rounded-sm bg-gray-50 px-2 py-1 font-mono text-xs text-gray-600 dark:bg-dark-800 dark:text-gray-300"
            >
              {{ redirectUrl }}
            </code>
            <p v-else class="text-xs text-amber-600 dark:text-amber-400">
              {{ t('admin.settings.socialOAuth.redirectUrlMissing') }}
            </p>
            <p class="mt-1.5 text-xs text-gray-500 dark:text-gray-400">
              {{ t('admin.settings.socialOAuth.redirectUrlHint', { provider: label }) }}
            </p>
          </div>

          <p class="text-xs text-gray-500 dark:text-gray-400">
            {{ t('admin.settings.socialOAuth.accountHint', { provider: label }) }}
          </p>
        </div>
      </div>
    </div>
  </div>
</template>

<script setup lang="ts">
import { useI18n } from 'vue-i18n'
import Toggle from '@/components/common/Toggle.vue'

/** Google / GitHub sign-in settings. The secret is write-only; the callback URL is derived by the server. */
defineProps<{ label: string; secretConfigured: boolean; redirectUrl: string }>()
const enabled = defineModel<boolean>('enabled', { required: true })
const clientId = defineModel<string>('clientId', { required: true })
const clientSecret = defineModel<string>('clientSecret', { required: true })
const { t } = useI18n()
</script>
