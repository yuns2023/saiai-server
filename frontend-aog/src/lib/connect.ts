/**
 * Connection instructions for a key (Claude Code only).
 *
 * The site's local client is AITP (github.com/2742zdz/AITP, binary `aitp`).
 * Everything client-specific lives in CLIENT below so a rename or a change to
 * the installer contract is a one-place edit.
 */

const shellQuote = (v: string) => `'${v.replace(/'/g, "'\\''")}'`
const psQuote = (v: string) => `'${v.replace(/'/g, "''")}'`

export type Shell = 'unix' | 'powershell' | 'cmd'

type InstallCommand = (root: string, key: string) => string

/** Where setup.sh / setup.ps1 are downloaded from (the latest AITP GitHub release). */
const INSTALLER_BASE = 'https://github.com/2742zdz/AITP/releases/latest/download'

/**
 * PowerShell line. When the root contains no double quote or % (true for the admin's
 * api_base_url in practice) and the key is [A-Za-z0-9_-], cmd can wrap it in "..." unchanged.
 */
const powershellInstall: InstallCommand = (root, key) =>
  `& ([scriptblock]::Create((irm ${INSTALLER_BASE}/setup.ps1))) -BaseUrl ${psQuote(root)} -ApiKey ${psQuote(key)}`

/**
 * One-line install per shell. `root` is the gateway root without /v1. The installer installs aitp, then runs
 * `aitp init --base-url … --api-key …` and `aitp start`. Keys are
 * [A-Za-z0-9_-] but are quoted anyway.
 */
const INSTALL: Record<Shell, InstallCommand> = {
  unix: (root, key) =>
    `curl -fsSL ${INSTALLER_BASE}/setup.sh | bash -s -- --base-url ${shellQuote(root)} --api-key ${shellQuote(key)}`,
  powershell: powershellInstall,
  cmd: (root, key) => `powershell -NoProfile -ExecutionPolicy Bypass -Command "${powershellInstall(root, key)}"`
}

export const CLIENT = {
  /** Name shown to users. */
  displayName: 'AITP',
  installerBase: INSTALLER_BASE,
  /** "What is this / how to uninstall" link. */
  repoUrl: 'https://github.com/2742zdz/AITP',
  uninstallCommand: 'aitp uninstall',
  /** Offer the one-line install command (manual values stay available as a fallback). */
  oneLineInstall: true,
  install: INSTALL
} as const

/** Gateway root without a trailing /v1 (Claude Code adds its own path). */
export function gatewayRoot(baseUrl: string): string {
  try {
    const u = new URL(baseUrl, window.location.origin)
    u.pathname = u.pathname.replace(/\/+$/, '').replace(/\/v1$/, '') || '/'
    return u.href.replace(/\/$/, '')
  } catch {
    return baseUrl.replace(/\/+$/, '').replace(/\/v1$/, '') || window.location.origin
  }
}

export interface ConnectSnippet {
  /** One-line AITP install + configure command, or null when it is switched off. */
  command: string | null
  /** Plain values for configuring Claude Code by hand. */
  baseUrl: string
  envLines: string[]
}

export interface ConnectOptions {
  /** Offer the one-line install command (defaults to CLIENT.oneLineInstall). */
  oneLine?: boolean
}

export function connectSnippet(rawBaseUrl: string, apiKey: string, shell: Shell, options: ConnectOptions = {}): ConnectSnippet {
  const oneLine = options.oneLine ?? CLIENT.oneLineInstall
  const root = gatewayRoot(rawBaseUrl)
  return {
    command: oneLine ? CLIENT.install[shell](root, apiKey) : null,
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
