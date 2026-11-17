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

/**
 * Where setup.sh / setup.ps1 are downloaded from: aitp.lol (AITP's GitHub Pages site), which serves the
 * latest release's installers after checking them against its signed manifest. The installer then
 * downloads the program from the GitHub release itself.
 */
const INSTALLER_BASE = 'https://aitp.lol'

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

/** Gateway root without a trailing /v1 (Claude Code adds its own path). Display/manual use only. */
export function gatewayRoot(baseUrl: string): string {
  return parseRoot(baseUrl) ?? (stripLineBreaks(baseUrl).replace(/\/+$/, '').replace(/\/v1$/, '') || window.location.origin)
}

const stripLineBreaks = (v: string) => v.replace(/[\r\n]/g, '')

/**
 * The gateway root as a URL-serialized absolute http(s) URL (quotes, spaces and
 * other unsafe characters percent-encoded; query and fragment dropped), or
 * null when the base URL is not one. Install commands are only built from this.
 */
export function parseRoot(baseUrl: string): string | null {
  if (/[\r\n]/.test(baseUrl)) return null
  let u: URL
  try {
    u = new URL(baseUrl)
  } catch {
    return null
  }
  if (u.protocol !== 'http:' && u.protocol !== 'https:') return null
  u.search = ''
  u.hash = ''
  u.pathname = u.pathname.replace(/\/+$/, '').replace(/\/v1$/, '') || '/'
  return u.href.replace(/\/$/, '')
}

/** Safe inside cmd's "...": no quote that would close it, no %VAR% expansion, single line. */
const cmdSafe = (v: string) => !/["%\r\n]/.test(v)

export interface ConnectSnippet {
  /** One-line AITP install + configure command; null when switched off or the inputs are unsafe. */
  command: string | null
  /** The site's API address is not an absolute http(s) URL, so no install command is offered. */
  badBaseUrl: boolean
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
  const safeRoot = parseRoot(rawBaseUrl)
  const keyOk = !/[\r\n]/.test(apiKey) && (shell !== 'cmd' || cmdSafe(apiKey))
  const rootOk = safeRoot !== null && (shell !== 'cmd' || cmdSafe(safeRoot))
  const root = safeRoot ?? gatewayRoot(rawBaseUrl)
  return {
    command: oneLine && safeRoot !== null && rootOk && keyOk ? CLIENT.install[shell](safeRoot, apiKey) : null,
    badBaseUrl: safeRoot === null,
    baseUrl: root,
    envLines: envLines(shell, [['ANTHROPIC_BASE_URL', root], ['ANTHROPIC_AUTH_TOKEN', stripLineBreaks(apiKey)]])
  }
}

/** Script an install code redeems to: the CMD tab runs the PowerShell script. */
export type InstallCodeKind = 'unix' | 'powershell'

export const installCodeKind = (shell: Shell): InstallCodeKind => (shell === 'unix' ? 'unix' : 'powershell')

/** One-time install codes: 12 Crockford base32 characters (the server's alphabet). */
export const isInstallCode = (v: unknown): v is string => typeof v === 'string' && /^[0-9A-HJKMNP-TV-Z]{12}$/.test(v)

/** scheme://host[:port] with nothing a shell would expand (an IPv6 literal's [] is a glob in zsh). */
const plainOrigin = (v: string) => /^https?:\/\/[A-Za-z0-9.-]+(:\d{1,5})?$/.test(v)

/**
 * Short command that redeems a one-time install code: POST <origin>/i/<code> returns the
 * install script for this shell, with the key filled in by the server. `origin` is the
 * page's own origin (the redeem endpoint lives on the Server that serves this page),
 * kept separate from the API root the installer configures.
 */
export function installCodeCommand(shell: Shell, origin: string, code: string): string {
  const url = `${origin}/i/${code}`
  if (shell === 'unix') return `curl -fsSL -X POST ${plainOrigin(origin) ? url : shellQuote(url)} | bash`
  const ps = `irm ${plainOrigin(origin) ? url : psQuote(url)} -Method Post | iex`
  return shell === 'cmd' ? `powershell -NoProfile -ExecutionPolicy Bypass -Command "${ps}"` : ps
}

/** cmd.exe: set "K=V" keeps spaces and & | < > literal for interactive use. */
const cmdQuote = (v: string) => `"${v}"`

function envLines(shell: Shell, pairs: Array<[string, string]>): string[] {
  // A value with a double quote cannot be written as cmd's set "K=V"; leave that line out.
  const usable = shell === 'cmd' ? pairs.filter(([, v]) => !v.includes('"')) : pairs
  return usable.map(([k, v]) => {
    if (shell === 'powershell') return `$env:${k}=${psQuote(v)}`
    if (shell === 'cmd') return `set ${cmdQuote(`${k}=${v}`)}`
    return `export ${k}=${shellQuote(v)}`
  })
}
