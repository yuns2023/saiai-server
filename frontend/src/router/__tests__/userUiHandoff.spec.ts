import { afterEach, describe, expect, it } from 'vitest'
import { isClassicOwnedPath, shouldHandOffToAOG } from '../userUiHandoff'

describe('userUiHandoff', () => {
  afterEach(() => {
    delete window.__SAIAI_USER_UI__
  })

  it('never hands off on classic sites', () => {
    expect(shouldHandOffToAOG('/dashboard', '/admin/users')).toBe(false)
    expect(shouldHandOffToAOG('/login', '/admin')).toBe(false)
  })

  it('hands customer pages and login to the AOG UI', () => {
    window.__SAIAI_USER_UI__ = 'aog'
    expect(shouldHandOffToAOG('/dashboard', '/admin/users')).toBe(true)
    expect(shouldHandOffToAOG('/login', '/admin')).toBe(true)
    expect(shouldHandOffToAOG('/profile', '/admin/settings')).toBe(true)
  })

  it('keeps admin console and setup entry in the classic SPA', () => {
    window.__SAIAI_USER_UI__ = 'aog'
    expect(shouldHandOffToAOG('/admin/users', '/admin')).toBe(false)
    expect(shouldHandOffToAOG('/admin', '/admin/users')).toBe(false)
    expect(shouldHandOffToAOG('/setup', '/setup')).toBe(false)
  })

  it('does not reload the URL the browser already shows', () => {
    window.__SAIAI_USER_UI__ = 'aog'
    expect(shouldHandOffToAOG('/dashboard', '/dashboard')).toBe(false)
  })

  it('matches the backend ownership rule', () => {
    expect(isClassicOwnedPath('/admin')).toBe(true)
    expect(isClassicOwnedPath('/admin/')).toBe(true)
    expect(isClassicOwnedPath('/adminx')).toBe(false)
    expect(isClassicOwnedPath('/setup')).toBe(true)
    expect(isClassicOwnedPath('/setup/x')).toBe(false)
  })
})
