// Redeem-history rows written when an admin adjusts, revokes, or resets a
// subscription (backend AdjustmentTypeAdminSub*). They are records of an
// operation, not redeemable codes.

export const ADMIN_SUB_ADJUST = 'admin_sub_adjust'
export const ADMIN_SUB_REVOKE = 'admin_sub_revoke'
export const ADMIN_SUB_RESET = 'admin_sub_reset'

export const ADMIN_SUBSCRIPTION_RECORD_TYPES = [ADMIN_SUB_ADJUST, ADMIN_SUB_REVOKE, ADMIN_SUB_RESET] as const

/** Max length of the optional admin note (shown to the user). Mirrors the backend. */
export const ADMIN_NOTES_MAX_LENGTH = 500

export type SubscriptionWindow = 'five_hour' | 'daily' | 'weekly' | 'monthly'

// Bits of an admin_sub_reset record value (backend SubscriptionReset*).
const RESET_WINDOW_BITS: Array<[SubscriptionWindow, number]> = [
  ['five_hour', 1],
  ['daily', 2],
  ['weekly', 4],
  ['monthly', 8]
]

export function isAdminSubscriptionRecord(type: string): boolean {
  return (ADMIN_SUBSCRIPTION_RECORD_TYPES as readonly string[]).includes(type)
}

/** Any record of an admin operation (balance, concurrency, subscription). */
export function isAdminOperationRecord(type: string): boolean {
  return type === 'admin_balance' || type === 'admin_concurrency' || isAdminSubscriptionRecord(type)
}

export function decodeResetWindows(value: number): SubscriptionWindow[] {
  const mask = Math.round(value)
  return RESET_WINDOW_BITS.filter(([, bit]) => (mask & bit) !== 0).map(([window]) => window)
}

interface RecordLike {
  type: string
  value: number
  validity_days?: number | null
  group?: { name: string } | null
}

type Translate = (key: string, params?: Record<string, unknown>) => string

export function adminSubscriptionRecordTitle(item: RecordLike, t: Translate): string {
  switch (item.type) {
    case ADMIN_SUB_ADJUST:
      return item.value >= 0 ? t('redeem.subscriptionExtendedAdmin') : t('redeem.subscriptionShortenedAdmin')
    case ADMIN_SUB_REVOKE:
      return t('redeem.subscriptionRevokedAdmin')
    case ADMIN_SUB_RESET:
      return t('redeem.subscriptionQuotaResetAdmin')
    default:
      return t('common.unknown')
  }
}

/** "+30 天 · CC1000", "剩余 2 天 · CC1000", "5小时、日 · CC1000". */
export function adminSubscriptionRecordValue(item: RecordLike, t: Translate): string {
  let text: string
  if (item.type === ADMIN_SUB_RESET) {
    const windows = decodeResetWindows(item.value)
    text = windows.map((w) => t(`redeem.resetWindows.${w}`)).join(t('redeem.resetWindows.separator'))
  } else if (item.type === ADMIN_SUB_REVOKE) {
    text = t('redeem.revokedRemainingDays', { days: Math.abs(Math.round(item.value)) })
  } else {
    // Whole days, except when the change was capped at the max expiration date.
    const days = Number.isInteger(item.value) ? item.value : Number(item.value.toFixed(2))
    text = `${days >= 0 ? '+' : ''}${days}${t('redeem.days')}`
  }
  const groupName = item.group?.name
  return groupName ? `${text} · ${groupName}` : text
}
