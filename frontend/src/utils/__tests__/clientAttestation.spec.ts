import { describe, expect, it } from 'vitest'
import { clientAttestationStatusFromCode, clientAttestationTone, summarizeClientAttestation } from '../clientAttestation'

describe('clientAttestation', () => {
  it('maps stored codes to statuses', () => {
    expect(clientAttestationStatusFromCode(1)).toBe('valid')
    expect(clientAttestationStatusFromCode(6)).toBe('unknown_device')
    expect(clientAttestationStatusFromCode(8)).toBe('unverified')
    expect(clientAttestationStatusFromCode(0)).toBeNull()
    expect(clientAttestationStatusFromCode(9)).toBeNull()
    expect(clientAttestationStatusFromCode(null)).toBeNull()
  })

  it('uses red only for wrong or revoked proofs', () => {
    expect(clientAttestationTone('valid')).toBe('success')
    expect(clientAttestationTone('missing')).toBe('warning')
    expect(clientAttestationTone('unverified')).toBe('warning')
    expect(clientAttestationTone('invalid')).toBe('danger')
    expect(clientAttestationTone('revoked')).toBe('danger')
  })

  it('summarizes counts compactly', () => {
    expect(summarizeClientAttestation(null)).toEqual({ kind: 'none' })
    expect(summarizeClientAttestation({})).toEqual({ kind: 'none' })
    expect(summarizeClientAttestation({ valid: 12 })).toEqual({ kind: 'all_valid', count: 12 })
    expect(summarizeClientAttestation({ missing: 2, valid: 5, replay: 0 })).toEqual({
      kind: 'mixed',
      entries: [
        { status: 'valid', count: 5 },
        { status: 'missing', count: 2 }
      ]
    })
  })
})
