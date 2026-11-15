import { describe, expect, it } from 'vitest'
import { linkify } from '../linkify'

describe('linkify', () => {
  it('keeps plain text as a single text segment', () => {
    expect(linkify('向管理员索取')).toEqual([{ text: '向管理员索取' }])
    expect(linkify('')).toEqual([])
  })

  it('links http(s) URLs and leaves trailing punctuation outside', () => {
    expect(linkify('加群获取：https://t.me/abc。或 http://x.y/z).')).toEqual([
      { text: '加群获取：' },
      { text: 'https://t.me/abc', href: 'https://t.me/abc' },
      { text: '。或 ' },
      { text: 'http://x.y/z', href: 'http://x.y/z' },
      { text: ').' }
    ])
  })

  it('keeps a closing bracket that belongs to the URL', () => {
    expect(linkify('见 https://en.wikipedia.org/wiki/Foo_(bar)。')).toEqual([
      { text: '见 ' },
      { text: 'https://en.wikipedia.org/wiki/Foo_(bar)', href: 'https://en.wikipedia.org/wiki/Foo_(bar)' },
      { text: '。' }
    ])
    expect(linkify('(https://x.y/a)')[1]).toEqual({ text: 'https://x.y/a', href: 'https://x.y/a' })
  })

  it('never links other schemes and never yields HTML', () => {
    const segs = linkify('javascript:alert(1) <img src=x onerror=alert(1)> data:text/html,x')
    expect(segs.every((s) => !s.href)).toBe(true)
    expect(segs.map((s) => s.text).join('')).toBe('javascript:alert(1) <img src=x onerror=alert(1)> data:text/html,x')
  })
})
