export const CLAUDE_SETUP_TOKEN_DEFAULT_EXPIRES_IN_SECONDS = 365 * 24 * 60 * 60

const CLAUDE_SETUP_TOKEN_PATTERN = /sk-[0-9A-Za-z][0-9A-Za-z._~+/=-]*/g
const CLAUDE_SETUP_TOKEN_EXACT_PATTERN = /^sk-[0-9A-Za-z][0-9A-Za-z._~+/=-]*$/

export function parseClaudeSetupTokens(input: string): string[] {
  return Array.from(input.matchAll(CLAUDE_SETUP_TOKEN_PATTERN), (match) => match[0])
}

export function extractClaudeSetupToken(input: string): string {
  return parseClaudeSetupTokens(input)[0] || ''
}

export function isLikelyClaudeSetupToken(token: string): boolean {
  return CLAUDE_SETUP_TOKEN_EXACT_PATTERN.test(token.trim())
}

export function buildClaudeSetupTokenCredentials(
  rawToken: string,
  nowUnixSeconds = Math.floor(Date.now() / 1000)
): Record<string, unknown> {
  const accessToken = extractClaudeSetupToken(rawToken)
  if (!accessToken) {
    throw new Error('Setup Token is required')
  }

  return {
    access_token: accessToken,
    token_type: 'Bearer',
    expires_in: CLAUDE_SETUP_TOKEN_DEFAULT_EXPIRES_IN_SECONDS,
    expires_at: nowUnixSeconds + CLAUDE_SETUP_TOKEN_DEFAULT_EXPIRES_IN_SECONDS,
    scope: 'user:inference'
  }
}
