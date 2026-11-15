import { describe, expect, it } from 'vitest'
import { CLIENT, connectSnippet, gatewayRoot } from '../connect'

describe('connect', () => {
  it('normalizes the gateway root (no /v1, no trailing slash)', () => {
    expect(gatewayRoot('https://ccyou.example/v1/')).toBe('https://ccyou.example')
    expect(gatewayRoot('https://ccyou.example/')).toBe('https://ccyou.example')
  })

  it('points the AITP client at the site', () => {
    expect(CLIENT.displayName).toBe('AITP')
    expect(CLIENT.installerBase).toBe('https://github.com/2742zdz/AITP/releases/latest/download')
    expect(CLIENT.oneLineInstall).toBe(true)
  })

  it('builds the exact install command for each shell', () => {
    const key = 'sk-aaa_BBB-000'
    expect(connectSnippet('https://ccyou.example/v1', key, 'unix').command).toBe(
      "curl -fsSL https://github.com/2742zdz/AITP/releases/latest/download/setup.sh | bash -s -- --base-url 'https://ccyou.example' --api-key 'sk-aaa_BBB-000'"
    )
    expect(connectSnippet('https://ccyou.example/v1', key, 'powershell').command).toBe(
      "& ([scriptblock]::Create((irm https://github.com/2742zdz/AITP/releases/latest/download/setup.ps1))) -BaseUrl 'https://ccyou.example' -ApiKey 'sk-aaa_BBB-000'"
    )
    expect(connectSnippet('https://ccyou.example/v1', key, 'cmd').command).toBe(
      'powershell -NoProfile -ExecutionPolicy Bypass -Command "& ([scriptblock]::Create((irm https://github.com/2742zdz/AITP/releases/latest/download/setup.ps1))) -BaseUrl \'https://ccyou.example\' -ApiKey \'sk-aaa_BBB-000\'"'
    )
  })

  it("quotes a key containing ' for each shell", () => {
    const key = "sk-it's"
    expect(connectSnippet('https://ccyou.example', key, 'unix').command).toBe(
      "curl -fsSL https://github.com/2742zdz/AITP/releases/latest/download/setup.sh | bash -s -- --base-url 'https://ccyou.example' --api-key 'sk-it'\\''s'"
    )
    expect(connectSnippet('https://ccyou.example', key, 'powershell').command).toBe(
      "& ([scriptblock]::Create((irm https://github.com/2742zdz/AITP/releases/latest/download/setup.ps1))) -BaseUrl 'https://ccyou.example' -ApiKey 'sk-it''s'"
    )
    const cmd = connectSnippet('https://ccyou.example', key, 'cmd').command!
    expect(cmd).toBe(
      'powershell -NoProfile -ExecutionPolicy Bypass -Command "& ([scriptblock]::Create((irm https://github.com/2742zdz/AITP/releases/latest/download/setup.ps1))) -BaseUrl \'https://ccyou.example\' -ApiKey \'sk-it\'\'s\'"'
    )
    // cmd wraps the whole PowerShell line in one pair of double quotes, so none may appear inside.
    expect(cmd.slice(cmd.indexOf('"') + 1, -1)).not.toContain('"')
  })

  it('keeps any gateway path in --base-url', () => {
    const s = connectSnippet('https://api.ccyou.example/relay/v1', 'k', 'unix')
    expect(s.command).toBe("curl -fsSL https://github.com/2742zdz/AITP/releases/latest/download/setup.sh | bash -s -- --base-url 'https://api.ccyou.example/relay' --api-key 'k'")
    expect(s.baseUrl).toBe('https://api.ccyou.example/relay')
  })

  it('can switch the one-line install off, leaving the manual Claude Code values', () => {
    for (const shell of ['unix', 'powershell', 'cmd'] as const) {
      const s = connectSnippet('https://ccyou.example', 'k', shell, { oneLine: false })
      expect(s.command).toBeNull()
      expect(s.envLines.length).toBe(2)
    }
  })

  it('offers only the Claude Code environment variables', () => {
    expect(connectSnippet('https://ccyou.example/v1', 'k', 'cmd').envLines).toEqual([
      'set "ANTHROPIC_BASE_URL=https://ccyou.example"',
      'set "ANTHROPIC_AUTH_TOKEN=k"'
    ])
  })

  it('quotes environment values so the shell cannot expand them', () => {
    const base = 'https://ccyou.example/a$b&c%d'
    expect(connectSnippet(base, 'k', 'unix').envLines[0]).toBe("export ANTHROPIC_BASE_URL='https://ccyou.example/a$b&c%d'")
    expect(connectSnippet(base, 'k', 'powershell').envLines[0]).toBe("$env:ANTHROPIC_BASE_URL='https://ccyou.example/a$b&c%d'")
    // Interactive cmd keeps %% literally, so the value is passed through inside the quotes.
    expect(connectSnippet(base, 'k', 'cmd').envLines[0]).toBe('set "ANTHROPIC_BASE_URL=https://ccyou.example/a$b&c%d"')
  })
})
