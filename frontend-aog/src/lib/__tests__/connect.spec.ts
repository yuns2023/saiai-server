import { describe, expect, it } from 'vitest'
import { connectSnippet, gatewayRoot, openAIBase } from '../connect'

describe('connect', () => {
  it('normalizes base URLs per client family', () => {
    expect(gatewayRoot('https://aog.example/v1/')).toBe('https://aog.example')
    expect(openAIBase('https://aog.example')).toBe('https://aog.example/v1')
    expect(openAIBase('https://aog.example/v1')).toBe('https://aog.example/v1')
  })

  it('quotes keys for each shell', () => {
    const key = "sk-it's"
    expect(connectSnippet('anthropic', 'https://aog.example', key, 'unix').command).toBe(
      "curl -fsSL https://aog.example/saiai-cli/setup.sh | bash -s -- 'https://aog.example' 'sk-it'\\''s'"
    )
    expect(connectSnippet('openai', 'https://aog.example', key, 'powershell').command).toBe(
      "irm https://aog.example/saiai-cli/setup.ps1 | iex; Invoke-Saiai init-codex 'https://aog.example/v1' 'sk-it''s'"
    )
  })

  it('has no one-line command for Gemini, only env vars', () => {
    const s = connectSnippet('gemini', 'https://aog.example', 'k', 'cmd')
    expect(s.command).toBeNull()
    expect(s.envLines).toEqual(['set "GOOGLE_GEMINI_BASE_URL=https://aog.example"', 'set "GEMINI_API_KEY=k"'])
  })

  it('quotes environment values so the shell cannot expand them', () => {
    const base = 'https://aog.example/a$b&c%d'
    expect(connectSnippet('anthropic', base, 'k', 'unix').envLines[0]).toBe("export ANTHROPIC_BASE_URL='https://aog.example/a$b&c%d'")
    expect(connectSnippet('anthropic', base, 'k', 'powershell').envLines[0]).toBe("$env:ANTHROPIC_BASE_URL='https://aog.example/a$b&c%d'")
    // Interactive cmd keeps %% literally, so the value is passed through inside the quotes.
    expect(connectSnippet('anthropic', base, 'k', 'cmd').envLines[0]).toBe('set "ANTHROPIC_BASE_URL=https://aog.example/a$b&c%d"')
  })
})
