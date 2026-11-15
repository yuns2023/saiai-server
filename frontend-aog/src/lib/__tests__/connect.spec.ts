import { describe, expect, it } from 'vitest'
import { CLIENT, connectSnippet, gatewayRoot } from '../connect'

describe('connect', () => {
  it('normalizes the gateway root (no /v1, no trailing slash)', () => {
    expect(gatewayRoot('https://ccyou.example/v1/')).toBe('https://ccyou.example')
    expect(gatewayRoot('https://ccyou.example/')).toBe('https://ccyou.example')
  })

  it('points the AITP client at the site', () => {
    expect(CLIENT.displayName).toBe('AITP')
    expect(CLIENT.installerBase).toBe('https://aitp.lol')
    expect(CLIENT.oneLineInstall).toBe(true)
  })

  it('builds the exact install command for each shell', () => {
    const key = 'sk-aaa_BBB-000'
    expect(connectSnippet('https://ccyou.example/v1', key, 'unix').command).toBe(
      "curl -fsSL https://aitp.lol/setup.sh | bash -s -- --base-url 'https://ccyou.example' --api-key 'sk-aaa_BBB-000'"
    )
    expect(connectSnippet('https://ccyou.example/v1', key, 'powershell').command).toBe(
      "& ([scriptblock]::Create((irm https://aitp.lol/setup.ps1))) -BaseUrl 'https://ccyou.example' -ApiKey 'sk-aaa_BBB-000'"
    )
    expect(connectSnippet('https://ccyou.example/v1', key, 'cmd').command).toBe(
      'powershell -NoProfile -ExecutionPolicy Bypass -Command "& ([scriptblock]::Create((irm https://aitp.lol/setup.ps1))) -BaseUrl \'https://ccyou.example\' -ApiKey \'sk-aaa_BBB-000\'"'
    )
  })

  it("quotes a key containing ' for each shell", () => {
    const key = "sk-it's"
    expect(connectSnippet('https://ccyou.example', key, 'unix').command).toBe(
      "curl -fsSL https://aitp.lol/setup.sh | bash -s -- --base-url 'https://ccyou.example' --api-key 'sk-it'\\''s'"
    )
    expect(connectSnippet('https://ccyou.example', key, 'powershell').command).toBe(
      "& ([scriptblock]::Create((irm https://aitp.lol/setup.ps1))) -BaseUrl 'https://ccyou.example' -ApiKey 'sk-it''s'"
    )
    const cmd = connectSnippet('https://ccyou.example', key, 'cmd').command!
    expect(cmd).toBe(
      'powershell -NoProfile -ExecutionPolicy Bypass -Command "& ([scriptblock]::Create((irm https://aitp.lol/setup.ps1))) -BaseUrl \'https://ccyou.example\' -ApiKey \'sk-it\'\'s\'"'
    )
    // cmd wraps the whole PowerShell line in one pair of double quotes, so none may appear inside.
    expect(cmd.slice(cmd.indexOf('"') + 1, -1)).not.toContain('"')
  })

  it('keeps any gateway path in --base-url', () => {
    const s = connectSnippet('https://api.ccyou.example/relay/v1', 'k', 'unix')
    expect(s.command).toBe("curl -fsSL https://aitp.lol/setup.sh | bash -s -- --base-url 'https://api.ccyou.example/relay' --api-key 'k'")
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

  it('never builds an install command from a base URL that is not an absolute http(s) URL', () => {
    const evil = 'https://[invalid]/" & echo X & rem "'
    for (const shell of ['unix', 'powershell', 'cmd'] as const) {
      const s = connectSnippet(evil, 'k', shell)
      expect(s.command).toBeNull()
      expect(s.badBaseUrl).toBe(true)
    }
    // cmd cannot carry a value with " inside set "K=V", so that line is left out.
    expect(connectSnippet(evil, 'k', 'cmd').envLines).toEqual(['set "ANTHROPIC_AUTH_TOKEN=k"'])
    for (const bad of ['/relative', 'ftp://ccyou.example', 'javascript:alert(1)', 'https://ccyou.example\r\n& calc', '']) {
      expect(connectSnippet(bad, 'k', 'unix').command).toBeNull()
    }
  })

  it('uses the URL-serialized root, so quotes and spaces in a valid URL are percent-encoded', () => {
    const s = connectSnippet('https://ccyou.example/a" b/v1?x="y"#"z"', 'k', 'powershell')
    expect(s.command).toBe(
      "& ([scriptblock]::Create((irm https://aitp.lol/setup.ps1))) -BaseUrl 'https://ccyou.example/a%22%20b' -ApiKey 'k'"
    )
    // The encoded form contains %, which cmd could expand, so cmd gets no one-liner.
    expect(connectSnippet('https://ccyou.example/a" b', 'k', 'cmd').command).toBeNull()
    expect(s.badBaseUrl).toBe(false)
  })

  it('skips only the cmd one-liner when cmd would expand % or the key has a quote or line break', () => {
    expect(connectSnippet('https://ccyou.example/%OS%', 'k', 'cmd').command).toBeNull()
    expect(connectSnippet('https://ccyou.example/%OS%', 'k', 'powershell').command).not.toBeNull()
    expect(connectSnippet('https://ccyou.example', 'a"b', 'cmd').command).toBeNull()
    expect(connectSnippet('https://ccyou.example', 'a\nb', 'unix').command).toBeNull()
  })
})
