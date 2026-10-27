import type { ClientAttestationStatus } from '@/types'

// Stored codes in usage_logs.client_attestation (docs/CLIENT_PROOF_V1.md).
export const CLIENT_ATTESTATION_STATUSES: ClientAttestationStatus[] = [
  'valid',
  'missing',
  'invalid',
  'stale',
  'replay',
  'unknown_device',
  'revoked',
  'unverified'
]

export const clientAttestationStatusFromCode = (code: number | null | undefined): ClientAttestationStatus | null => {
  if (typeof code !== 'number' || !Number.isInteger(code)) return null
  return CLIENT_ATTESTATION_STATUSES[code - 1] ?? null
}

// Red: the proof was present but wrong or revoked. Amber: absent or not
// conclusive (clock skew, repeat, infrastructure). Valid stays green.
const RED_STATUSES = new Set<ClientAttestationStatus>(['invalid', 'unknown_device', 'revoked'])

export const clientAttestationTone = (status: ClientAttestationStatus): 'success' | 'warning' | 'danger' => {
  if (status === 'valid') return 'success'
  return RED_STATUSES.has(status) ? 'danger' : 'warning'
}

export const clientAttestationBadgeClass = (status: ClientAttestationStatus): string => {
  switch (clientAttestationTone(status)) {
    case 'success':
      return 'bg-emerald-100 text-emerald-700 dark:bg-emerald-900/30 dark:text-emerald-300'
    case 'danger':
      return 'bg-red-100 text-red-700 dark:bg-red-900/30 dark:text-red-300'
    default:
      return 'bg-amber-100 text-amber-800 dark:bg-amber-900/30 dark:text-amber-300'
  }
}

export interface ClientAttestationSummaryEntry {
  status: ClientAttestationStatus
  count: number
}

export type ClientAttestationSummary =
  | { kind: 'none' }
  | { kind: 'all_valid'; count: number }
  | { kind: 'mixed'; entries: ClientAttestationSummaryEntry[] }

// Compact per-user summary: "all valid" collapses to one green badge;
// otherwise every non-zero non-valid status is listed (valid count first).
export const summarizeClientAttestation = (
  counts: Partial<Record<ClientAttestationStatus, number>> | null | undefined
): ClientAttestationSummary => {
  const entries = CLIENT_ATTESTATION_STATUSES
    .map((status) => ({ status, count: Number(counts?.[status] ?? 0) }))
    .filter((entry) => entry.count > 0)
  if (entries.length === 0) return { kind: 'none' }
  if (entries.length === 1 && entries[0].status === 'valid') return { kind: 'all_valid', count: entries[0].count }
  return { kind: 'mixed', entries }
}
