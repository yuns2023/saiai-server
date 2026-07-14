import { describe, expect, it } from 'vitest'
import {
  buildClaudeSetupTokenCredentials,
  CLAUDE_SETUP_TOKEN_DEFAULT_EXPIRES_IN_SECONDS,
  isLikelyClaudeSetupToken,
  parseClaudeSetupTokens
} from '../claudeSetupToken'

describe('claudeSetupToken', () => {
  it('parses one token per line and trims blanks', () => {
    expect(parseClaudeSetupTokens('\n sk-ant-oat01-a \r\nsk-rec-example-b\n')).toEqual([
      'sk-ant-oat01-a',
      'sk-rec-example-b'
    ])
  })

  it('extracts a token from full Claude CLI output', () => {
    const raw = `Long-lived authentication token created successfully!

Your OAuth token:

sk-ant-oat01-example-token`

    expect(parseClaudeSetupTokens(raw)).toEqual(['sk-ant-oat01-example-token'])
    expect(buildClaudeSetupTokenCredentials(raw, 1_700_000_000).access_token).toBe(
      'sk-ant-oat01-example-token'
    )
  })

  it('recognizes supported sk token prefixes', () => {
    expect(isLikelyClaudeSetupToken(' sk-ant-oat01-example ')).toBe(true)
    expect(isLikelyClaudeSetupToken('sk-rec-example')).toBe(true)
    expect(isLikelyClaudeSetupToken('sk-ant-sid01-example')).toBe(true)
    expect(isLikelyClaudeSetupToken('not-a-token')).toBe(false)
  })

  it('builds setup-token account credentials without a refresh token', () => {
    const credentials = buildClaudeSetupTokenCredentials('sk-ant-oat01-example', 1_700_000_000)

    expect(credentials).toEqual({
      access_token: 'sk-ant-oat01-example',
      token_type: 'Bearer',
      expires_in: CLAUDE_SETUP_TOKEN_DEFAULT_EXPIRES_IN_SECONDS,
      expires_at: 1_700_000_000 + CLAUDE_SETUP_TOKEN_DEFAULT_EXPIRES_IN_SECONDS,
      scope: 'user:inference'
    })
    expect(credentials).not.toHaveProperty('refresh_token')
  })

  it('builds setup-token credentials from other sk-prefixed tokens', () => {
    const credentials = buildClaudeSetupTokenCredentials('sk-rec-example', 1_700_000_000)

    expect(credentials.access_token).toBe('sk-rec-example')
    expect(credentials.scope).toBe('user:inference')
  })

  it('rejects non sk-prefixed input', () => {
    expect(() => buildClaudeSetupTokenCredentials('not-a-token')).toThrow('Setup Token is required')
  })
})
