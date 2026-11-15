import type { PublicSettings } from '@/types'

declare global {
  interface Window {
    __APP_CONFIG__?: PublicSettings
    __SAIAI_USER_UI__?: string
  }
}

export {}
