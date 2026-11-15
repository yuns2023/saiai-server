import type { PublicSettings } from '@/types'

declare global {
  interface Window {
    __APP_CONFIG__?: PublicSettings
    /** Set by the backend when SERVER_USER_UI selects a non-classic customer UI. */
    __SAIAI_USER_UI__?: string
  }
}

export {}
