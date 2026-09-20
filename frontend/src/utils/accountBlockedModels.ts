export function parseAccountBlockedModelPatterns(value: string): string[] {
  const seen = new Set<string>()
  for (const line of value.split(/\r?\n/)) {
    const pattern = line.trim().toLowerCase()
    if (pattern) seen.add(pattern)
  }
  return Array.from(seen)
}

export function formatAccountBlockedModelPatterns(value: unknown): string {
  if (!Array.isArray(value)) return ''
  return value
    .filter((item): item is string => typeof item === 'string')
    .map((item) => item.trim())
    .filter(Boolean)
    .join('\n')
}
