import { describe, expect, it } from 'vitest'
import {
  adminSubscriptionRecordTitle,
  adminSubscriptionRecordValue,
  decodeResetWindows,
  isAdminOperationRecord,
  isAdminSubscriptionRecord,
  isRedeemPurchaseRecord
} from '../adminSubscriptionRecords'

const t = (key: string, params?: Record<string, unknown>) =>
  params ? `${key}(${JSON.stringify(params)})` : key

describe('adminSubscriptionRecords', () => {
  it('classifies record types', () => {
    expect(isAdminSubscriptionRecord('admin_sub_adjust')).toBe(true)
    expect(isAdminSubscriptionRecord('subscription')).toBe(false)
    expect(isAdminOperationRecord('admin_balance')).toBe(true)
    expect(isAdminOperationRecord('admin_sub_reset')).toBe(true)
    expect(isAdminOperationRecord('balance')).toBe(false)
    expect(isRedeemPurchaseRecord('purchase_debit')).toBe(true)
    expect(isRedeemPurchaseRecord('purchase_refund')).toBe(true)
    expect(isRedeemPurchaseRecord('balance')).toBe(false)
    expect(isAdminOperationRecord('purchase_debit')).toBe(false)
  })

  it('decodes reset window bits in a fixed order', () => {
    expect(decodeResetWindows(1)).toEqual(['five_hour'])
    expect(decodeResetWindows(6)).toEqual(['daily', 'weekly'])
    expect(decodeResetWindows(15)).toEqual(['five_hour', 'daily', 'weekly', 'monthly'])
    expect(decodeResetWindows(0)).toEqual([])
  })

  it('titles adjust records by direction', () => {
    expect(adminSubscriptionRecordTitle({ type: 'admin_sub_adjust', value: 30 }, t)).toBe('redeem.subscriptionExtendedAdmin')
    expect(adminSubscriptionRecordTitle({ type: 'admin_sub_adjust', value: -5 }, t)).toBe('redeem.subscriptionShortenedAdmin')
    expect(adminSubscriptionRecordTitle({ type: 'admin_sub_revoke', value: -2 }, t)).toBe('redeem.subscriptionRevokedAdmin')
    expect(adminSubscriptionRecordTitle({ type: 'admin_sub_reset', value: 2 }, t)).toBe('redeem.subscriptionQuotaResetAdmin')
  })

  it('formats values with the group name', () => {
    const group = { name: 'CC1000' }
    expect(adminSubscriptionRecordValue({ type: 'admin_sub_adjust', value: 30, group }, t)).toBe('+30redeem.days · CC1000')
    expect(adminSubscriptionRecordValue({ type: 'admin_sub_adjust', value: -5 }, t)).toBe('-5redeem.days')
    expect(adminSubscriptionRecordValue({ type: 'admin_sub_adjust', value: 0.25 }, t)).toBe('+0.25redeem.days')
    expect(adminSubscriptionRecordValue({ type: 'admin_sub_revoke', value: -2, group }, t)).toBe(
      'redeem.revokedRemainingDays({"days":2}) · CC1000'
    )
    expect(adminSubscriptionRecordValue({ type: 'admin_sub_reset', value: 3 }, t)).toBe(
      'redeem.resetWindows.five_hourredeem.resetWindows.separatorredeem.resetWindows.daily'
    )
  })
})
