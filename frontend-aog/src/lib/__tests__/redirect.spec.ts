import { describe, expect, it } from 'vitest'
import { isClassicOwnedPath, postLoginTarget, safeRedirectTarget } from '../redirect'

const origin = 'https://aog.example'

describe('safeRedirectTarget', () => {
  it('keeps same-origin paths with query and hash', () => {
    expect(safeRedirectTarget('/admin/users?page=2#x', origin)).toBe('/admin/users?page=2#x')
    expect(safeRedirectTarget('/usage', origin)).toBe('/usage')
  })

  it('rejects anything that can leave the site', () => {
    for (const bad of ['//evil.example', '/\\evil.example', 'https://evil.example/', 'javascript:alert(1)', 'usage', '', '/\u0000x', 42, null]) {
      expect(safeRedirectTarget(bad, origin)).toBeNull()
    }
  })

  it('normalizes dotted and encoded paths without leaving the origin', () => {
    expect(safeRedirectTarget('/a/../admin', origin)).toBe('/admin')
    expect(safeRedirectTarget('/%2F%2Fevil.example', origin)).toBe('/%2F%2Fevil.example')
  })
})

describe('postLoginTarget', () => {
  it('sends admins to the classic console with a full page load', () => {
    expect(postLoginTarget('/admin/users?page=2', true)).toEqual({ path: '/admin/users?page=2', fullPage: true })
    expect(postLoginTarget(undefined, true)).toEqual({ path: '/admin/dashboard', fullPage: true })
  })

  it('keeps users in this app', () => {
    expect(postLoginTarget('/admin/users', false)).toEqual({ path: '/dashboard', fullPage: false })
    expect(postLoginTarget('/usage?x=1', false)).toEqual({ path: '/usage?x=1', fullPage: false })
    expect(postLoginTarget('//evil.example', false)).toEqual({ path: '/dashboard', fullPage: false })
    expect(postLoginTarget('/login', false)).toEqual({ path: '/dashboard', fullPage: false })
  })

  it('classifies encoded console paths the way the server does', () => {
    expect(postLoginTarget('/%61dmin/users', true)).toEqual({ path: '/%61dmin/users', fullPage: true })
    expect(postLoginTarget('/admin%2Fusers', true)).toEqual({ path: '/admin%2Fusers', fullPage: true })
    expect(postLoginTarget('/%61dmin/users', false)).toEqual({ path: '/dashboard', fullPage: false })
    expect(postLoginTarget('/%E0%A4%A', false)).toEqual({ path: '/dashboard', fullPage: false })
    // Decodes to /x/../admin/users, which the server would clean to the console.
    expect(postLoginTarget('/x%2F..%2Fadmin/users', false)).toEqual({ path: '/dashboard', fullPage: false })
    expect(postLoginTarget('/x%2F..%2Fadmin/users', true)).toEqual({ path: '/admin/dashboard', fullPage: true })
  })

  it('matches the backend page ownership rule', () => {
    expect(isClassicOwnedPath('/admin')).toBe(true)
    expect(isClassicOwnedPath('/adminx')).toBe(false)
    expect(isClassicOwnedPath('/setup')).toBe(true)
    expect(isClassicOwnedPath('/setup/x')).toBe(false)
  })
})
