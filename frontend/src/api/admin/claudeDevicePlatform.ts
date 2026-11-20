/**
 * Claude device platform observation (admin, read-only):
 * /api/v1/admin/claude-device-platform/flagged.
 * Per site, last 30 days. Never shows the full device id, only an 8-hex prefix.
 */
import { apiClient } from '../client'

export type ClaudeDeviceFlagReason =
  | 'cross_family'
  | 'version_alternation'
  | 'cross_user'
  | 'arch_only'
  | 'sequential_upgrade'

export const CLAUDE_DEVICE_FLAG_REASONS: ClaudeDeviceFlagReason[] = [
  'cross_family',
  'version_alternation',
  'cross_user',
  'arch_only',
  'sequential_upgrade'
]

export const CLAUDE_DEVICE_DEFAULT_REASONS: ClaudeDeviceFlagReason[] = ['cross_family', 'version_alternation', 'cross_user']

export interface ClaudeDeviceReasonMatch {
  reason: ClaudeDeviceFlagReason | string
  last_matched_at: string
  evidence: string
}

export interface ClaudeDevicePlatformEntry {
  family: string
  arch: string
  first_seen_at: string
  last_seen_at: string
}

export interface ClaudeDeviceVersionEntry {
  family: string
  major: string
  first_seen_at: string
  last_seen_at: string
}

export interface ClaudeDeviceUser {
  user_id: number
  username: string
  email: string
  api_key_id: number
  last_seen_at: string
}

export interface ClaudeFlaggedDevice {
  display_prefix: string
  last_matched_at: string
  reasons: ClaudeDeviceReasonMatch[]
  platforms: ClaudeDevicePlatformEntry[]
  versions: ClaudeDeviceVersionEntry[]
  users: ClaudeDeviceUser[]
  users_truncated: boolean
}

/** Process-local counters of the Gateway instance that answered. */
export interface ClaudeDevicePlatformStats {
  enqueued: number
  written: number
  dedupe_skipped: number
  queue_full: number
  invalid_device_id: number
  parse_overflow: number
  global_cap: number
  user_cap: number
  no_owner: number
  write_errors: number
}

export interface ClaudeFlaggedDevicesPage {
  items: ClaudeFlaggedDevice[]
  /** '' = no more pages. */
  next_cursor: string
  reasons: string[]
  stats: ClaudeDevicePlatformStats
}

export const claudeDevicePlatformAPI = {
  async listFlagged(params: { reasons: ClaudeDeviceFlagReason[]; cursor?: string; limit?: number }): Promise<ClaudeFlaggedDevicesPage> {
    const query: Record<string, string | number> = { reasons: params.reasons.join(',') }
    if (params.cursor) query.cursor = params.cursor
    if (params.limit) query.limit = params.limit
    const { data } = await apiClient.get<ClaudeFlaggedDevicesPage>('/admin/claude-device-platform/flagged', { params: query })
    return { ...data, items: data?.items ?? [], next_cursor: data?.next_cursor ?? '' }
  }
}

export default claudeDevicePlatformAPI
