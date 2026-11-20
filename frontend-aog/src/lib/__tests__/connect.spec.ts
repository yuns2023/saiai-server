import { describe, expect, it } from 'vitest'
import { CLIENT, connectSnippet, installCodeCommand, installCodeKind, isInstallCode } from '../connect'

describe('connect', () => {
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
  })

  it('can switch the one-line install off', () => {
    for (const shell of ['unix', 'powershell', 'cmd'] as const) {
      const s = connectSnippet('https://ccyou.example', 'k', shell, { oneLine: false })
      expect(s.command).toBeNull()
      expect(Object.keys(s).sort()).toEqual(['badBaseUrl', 'command'])
    }
  })

  it('never builds an install command from a base URL that is not an absolute http(s) URL', () => {
    const evil = 'https://[invalid]/" & echo X & rem "'
    for (const shell of ['unix', 'powershell', 'cmd'] as const) {
      const s = connectSnippet(evil, 'k', shell)
      expect(s.command).toBeNull()
      expect(s.badBaseUrl).toBe(true)
    }
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

  it('builds the exact one-time install code command for each shell', () => {
    const origin = 'https://ccyou.dev'
    expect(installCodeCommand('unix', origin, 'Q7M2K9XW4TRB')).toBe('curl -fsSL -X POST https://ccyou.dev/i/Q7M2K9XW4TRB | bash')
    expect(installCodeCommand('powershell', origin, 'Q7M2K9XW4TRB')).toBe('irm https://ccyou.dev/i/Q7M2K9XW4TRB -Method Post | iex')
    expect(installCodeCommand('cmd', origin, 'Q7M2K9XW4TRB')).toBe(
      'powershell -NoProfile -ExecutionPolicy Bypass -Command "irm https://ccyou.dev/i/Q7M2K9XW4TRB -Method Post | iex"'
    )
    expect(installCodeCommand('unix', 'http://127.0.0.1:8080', 'Q7M2K9XW4TRB')).toBe('curl -fsSL -X POST http://127.0.0.1:8080/i/Q7M2K9XW4TRB | bash')
  })

  it('quotes an origin a shell could expand (an IPv6 literal is a glob in zsh)', () => {
    expect(installCodeCommand('unix', 'http://[::1]:8080', 'Q7M2K9XW4TRB')).toBe("curl -fsSL -X POST 'http://[::1]:8080/i/Q7M2K9XW4TRB' | bash")
    expect(installCodeCommand('powershell', 'http://[::1]:8080', 'Q7M2K9XW4TRB')).toBe("irm 'http://[::1]:8080/i/Q7M2K9XW4TRB' -Method Post | iex")
  })

  it('maps shells to the script kind and recognizes codes', () => {
    expect(installCodeKind('unix')).toBe('unix')
    expect(installCodeKind('powershell')).toBe('powershell')
    expect(installCodeKind('cmd')).toBe('powershell')
    expect(isInstallCode('Q7M2K9XW4TRB')).toBe(true)
    expect(isInstallCode('0123456789AB')).toBe(true)
    for (const bad of ['', 'q7m2k9xw4trb', 'Q7M2K9XW4TR', 'Q7M2K9XW4TRBX', 'Q7M2K9XW4TRI', 'Q7M2K9XW4TRO', "Q7M2K9XW4T'B", 'Q7M2K9XW4T B', null, 12]) {
      expect(isInstallCode(bad)).toBe(false)
    }
  })
})
