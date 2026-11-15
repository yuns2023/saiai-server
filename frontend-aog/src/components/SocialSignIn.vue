<script setup lang="ts">
import { computed } from 'vue'
import { useI18n } from 'vue-i18n'
import { useAppStore } from '@/stores/app'
import { oauthStartUrl, type SocialProvider } from '@/lib/oauth'

/** "Sign in with Google / GitHub" buttons; renders nothing unless the site enabled a provider. */
const props = defineProps<{ redirect?: unknown }>()
const { t } = useI18n()
const app = useAppStore()

const providers = computed(() => {
  const s = app.settings
  const list: Array<{ id: SocialProvider; label: string }> = []
  if (s?.google_oauth_enabled) list.push({ id: 'google', label: t('auth.continueWithGoogle') })
  if (s?.github_oauth_enabled) list.push({ id: 'github', label: t('auth.continueWithGitHub') })
  return list
})
</script>

<template>
  <div v-if="providers.length" class="mt-5">
    <p class="flex items-center gap-3 text-xs text-ink-3" aria-hidden="true">
      <span class="h-px flex-1 bg-rule-soft" />{{ t('auth.orDivider') }}<span class="h-px flex-1 bg-rule-soft" />
    </p>
    <div class="mt-4 space-y-2">
      <a
        v-for="p in providers"
        :key="p.id"
        :href="oauthStartUrl(p.id, props.redirect)"
        class="btn btn-line h-10 w-full gap-2 no-underline"
        :data-provider="p.id"
      >
        <svg v-if="p.id === 'google'" class="h-4 w-4 shrink-0" viewBox="0 0 24 24" aria-hidden="true">
          <path fill="#4285F4" d="M23.5 12.3c0-.8-.1-1.6-.2-2.3H12v4.5h6.5a5.6 5.6 0 0 1-2.4 3.6v3h3.9c2.3-2.1 3.5-5.2 3.5-8.8z" />
          <path fill="#34A853" d="M12 24c3.2 0 6-1.1 8-2.9l-3.9-3c-1.1.7-2.5 1.2-4.1 1.2-3.1 0-5.8-2.1-6.7-5H1.3v3.1A12 12 0 0 0 12 24z" />
          <path fill="#FBBC05" d="M5.3 14.3a7.2 7.2 0 0 1 0-4.6V6.6H1.3a12 12 0 0 0 0 10.8l4-3.1z" />
          <path fill="#EA4335" d="M12 4.8c1.8 0 3.3.6 4.6 1.8l3.4-3.4A12 12 0 0 0 1.3 6.6l4 3.1c.9-2.9 3.6-4.9 6.7-4.9z" />
        </svg>
        <svg v-else class="h-4 w-4 shrink-0" viewBox="0 0 24 24" aria-hidden="true">
          <path
            fill="currentColor"
            d="M12 .5a11.5 11.5 0 0 0-3.6 22.4c.6.1.8-.3.8-.6v-2c-3.2.7-3.9-1.5-3.9-1.5-.5-1.3-1.3-1.7-1.3-1.7-1-.7.1-.7.1-.7 1.2.1 1.8 1.2 1.8 1.2 1 1.8 2.8 1.3 3.5 1 .1-.8.4-1.3.7-1.6-2.6-.3-5.3-1.3-5.3-5.7 0-1.3.5-2.3 1.2-3.1-.1-.3-.5-1.5.1-3.1 0 0 1-.3 3.2 1.2a11 11 0 0 1 5.8 0c2.2-1.5 3.2-1.2 3.2-1.2.6 1.6.2 2.8.1 3.1.8.8 1.2 1.9 1.2 3.1 0 4.4-2.7 5.4-5.3 5.7.4.4.8 1.1.8 2.2v3.3c0 .3.2.7.8.6A11.5 11.5 0 0 0 12 .5z"
          />
        </svg>
        <span>{{ p.label }}</span>
      </a>
    </div>
  </div>
</template>
