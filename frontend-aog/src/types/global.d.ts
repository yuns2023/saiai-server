declare global {
  interface Window {
    __APP_CONFIG__?: { site_name?: string } & Record<string, unknown>
    __SAIAI_USER_UI__?: string
  }
}

export {}
