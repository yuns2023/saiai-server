import { describe, expect, it } from 'vitest'
import { ALLOWED_PLATFORMS, allowedGroups, isAllowedPlatform } from '../platforms'

describe('platforms', () => {
  it('offers Claude (anthropic) only', () => {
    expect(ALLOWED_PLATFORMS).toEqual(['anthropic'])
    expect(isAllowedPlatform('anthropic')).toBe(true)
    for (const p of ['openai', 'gemini', 'antigravity', 'sora', '', null, undefined]) expect(isAllowedPlatform(p)).toBe(false)
  })

  it('drops groups of other platforms', () => {
    const groups = [
      { id: 1, platform: 'anthropic' },
      { id: 2, platform: 'openai' },
      { id: 3, platform: 'gemini' },
      { id: 4, platform: 'antigravity' }
    ]
    expect(allowedGroups(groups).map((g) => g.id)).toEqual([1])
  })
})
