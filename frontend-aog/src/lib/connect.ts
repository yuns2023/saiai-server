/**
 * Connection instructions for a key.
 *
 * The client is being renamed from "saiai" to "aog" and may move to its own
 * open-source project. Everything client-specific lives in CLIENT below so the
 * swap is a one-place change once the aog bundle ships.
 */

export const CLIENT = {
  /** Name shown to users. */
  displayName: 'aog',
  /** Download path served by the gateway (SAIAI_CLIENT_DIR). */
  bundlePath: '/saiai-cli',
  /** PowerShell entry function defined by setup.ps1. */
  powershellEntry: 'Invoke-Saiai'
} as const

export type Platform = 'anthropic' | 'openai' | 'gemini' | string
export type Shell = 'unix' | 'powershell' | 'cmd'

const shellQuote = (v: string) => `'${v.replace(/'/g, "'\\''")}'`
const psQuote = (v: string) => `'${v.replace(/'/g, "''")}'`

export function cliOrigin(baseUrl: string): string {
  try {
    return new URL(baseUrl, window.location.origin).origin
  } catch {
    return window.location.origin
  }
}

/** Gateway root without a trailing /v1 (Anthropic clients add their own path). */
export function gatewayRoot(baseUrl: string): string {
  try {
    const u = new URL(baseUrl, window.location.origin)
    u.pathname = u.pathname.replace(/\/+$/, '').replace(/\/v1$/, '') || '/'
    return u.href.replace(/\/$/, '')
  } catch {
    return baseUrl.replace(/\/+$/, '').replace(/\/v1$/, '') || window.location.origin
  }
}

/** OpenAI-compatible base URL ending in /v1. */
export function openAIBase(baseUrl: string): string {
  try {
    const u = new URL(baseUrl, window.location.origin)
    const p = u.pathname.replace(/\/+$/, '')
    u.pathname = p.endsWith('/v1') ? p : `${p}/v1`
    return u.href.replace(/\/$/, '')
  } catch {
    const n = baseUrl.replace(/\/+$/, '')
    return n.endsWith('/v1') ? n : `${n}/v1`
  }
}

function bootstrap(shell: Shell, origin: string, unixArgs: string, psArgs: string): string {
  const base = `${origin}${CLIENT.bundlePath}`
  const ps = `irm ${base}/setup.ps1 | iex; ${CLIENT.powershellEntry} ${psArgs}`
  switch (shell) {
    case 'unix':
      return unixArgs ? `curl -fsSL ${base}/setup.sh | bash -s -- ${unixArgs}` : `curl -fsSL ${base}/setup.sh | bash`
    case 'powershell':
      return ps
    case 'cmd':
      return `powershell -NoProfile -ExecutionPolicy Bypass -Command "${ps}"`
  }
}

export interface ConnectSnippet {
  /** One-line client install + configure command, when the platform has one. */
  command: string | null
  /** Plain values for tools configured by hand. */
  baseUrl: string
  envLines: string[]
}

export function connectSnippet(platform: Platform, rawBaseUrl: string, apiKey: string, shell: Shell): ConnectSnippet {
  const origin = cliOrigin(rawBaseUrl)
  if (platform === 'openai') {
    const base = openAIBase(rawBaseUrl)
    return {
      command: bootstrap(shell, origin, `init-codex ${shellQuote(base)} ${shellQuote(apiKey)}`, `init-codex ${psQuote(base)} ${psQuote(apiKey)}`),
      baseUrl: base,
      envLines: envLines(shell, [['OPENAI_BASE_URL', base], ['OPENAI_API_KEY', apiKey]])
    }
  }
  if (platform === 'gemini') {
    const base = rawBaseUrl.replace(/\/+$/, '')
    return {
      command: null,
      baseUrl: base,
      envLines: envLines(shell, [['GOOGLE_GEMINI_BASE_URL', base], ['GEMINI_API_KEY', apiKey]])
    }
  }
  const root = gatewayRoot(rawBaseUrl)
  return {
    command: bootstrap(shell, origin, `${shellQuote(root)} ${shellQuote(apiKey)}`, `${psQuote(root)} ${psQuote(apiKey)}`),
    baseUrl: root,
    envLines: envLines(shell, [['ANTHROPIC_BASE_URL', root], ['ANTHROPIC_AUTH_TOKEN', apiKey]])
  }
}

/** cmd.exe: set "K=V" keeps spaces and & | < > literal for interactive use. */
const cmdQuote = (v: string) => `"${v}"`

function envLines(shell: Shell, pairs: Array<[string, string]>): string[] {
  return pairs.map(([k, v]) => {
    if (shell === 'powershell') return `$env:${k}=${psQuote(v)}`
    if (shell === 'cmd') return `set ${cmdQuote(`${k}=${v}`)}`
    return `export ${k}=${shellQuote(v)}`
  })
}
