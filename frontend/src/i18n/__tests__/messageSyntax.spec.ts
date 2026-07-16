import { describe, expect, it } from 'vitest'

import en from '../locales/en'
import zh from '../locales/zh'

function collectStrings(value: unknown, path = ''): Array<{ path: string; value: string }> {
  if (typeof value === 'string') {
    return [{ path, value }]
  }

  if (!value || typeof value !== 'object') {
    return []
  }

  return Object.entries(value).flatMap(([key, child]) =>
    collectStrings(child, path ? `${path}.${key}` : key)
  )
}

describe('i18n message syntax', () => {
  it('does not contain accidental vue-i18n linked-message markers', () => {
    const messages = [
      ...collectStrings(en, 'en'),
      ...collectStrings(zh, 'zh')
    ]
    const accidentalLinkedMessages = messages.filter(({ value }) => /@[A-Za-z_]/.test(value))

    expect(accidentalLinkedMessages).toEqual([])
  })
})
