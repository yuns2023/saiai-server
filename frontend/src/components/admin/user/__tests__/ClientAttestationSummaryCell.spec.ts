import { describe, expect, it, vi } from 'vitest'
import { mount } from '@vue/test-utils'

vi.mock('vue-i18n', async () => ({ ...await vi.importActual<typeof import('vue-i18n')>('vue-i18n'), useI18n: () => ({ t: (key: string) => key }) }))
import ClientAttestationSummaryCell from '../ClientAttestationSummaryCell.vue'

describe('ClientAttestationSummaryCell', () => {
  it('collapses all-valid counts into one green badge', () => {
    const wrapper = mount(ClientAttestationSummaryCell, { props: { counts: { valid: 9 } } })
    const badge = wrapper.get('[data-testid="client-attestation-all-valid"]')
    expect(badge.text()).toBe('admin.users.clientProof.allValid')
    expect(badge.classes().join(' ')).toContain('emerald')
    expect(badge.attributes('title')).toContain('admin.users.clientProof.attestationHint')
  })

  it('lists non-valid statuses with amber/red tones', () => {
    const wrapper = mount(ClientAttestationSummaryCell, { props: { counts: { valid: 3, missing: 2, revoked: 1 } } })
    const text = wrapper.get('[data-testid="client-attestation-mixed"]').text()
    expect(text).toContain('admin.users.clientProof.status.missing 2')
    expect(text).toContain('admin.users.clientProof.status.revoked 1')
    const html = wrapper.html()
    expect(html).toContain('amber')
    expect(html).toContain('red')
  })

  it('renders a dash for no data or unavailable aggregates', () => {
    expect(mount(ClientAttestationSummaryCell, { props: { counts: {} } }).text()).toBe('-')
    const unavailable = mount(ClientAttestationSummaryCell, { props: { counts: null } })
    expect(unavailable.text()).toBe('-')
    expect(unavailable.get('span').attributes('title')).toContain('admin.users.clientProof.unavailable')
  })
})
