import { describe, expect, it } from 'vitest'
import zh from '../locales/zh'
import en from '../locales/en'

/** Every message path ("landing.payTitle") in a locale tree. */
function paths(tree: object, prefix = ''): string[] {
  return Object.entries(tree).flatMap(([k, v]) => (v && typeof v === 'object' ? paths(v, `${prefix}${k}.`) : [`${prefix}${k}`]))
}

describe('locales', () => {
  it('zh and en carry the same message keys', () => {
    const zhKeys = paths(zh).sort()
    const enKeys = paths(en).sort()
    expect(enKeys.filter((k) => !zhKeys.includes(k))).toEqual([])
    expect(zhKeys.filter((k) => !enKeys.includes(k))).toEqual([])
  })
})
