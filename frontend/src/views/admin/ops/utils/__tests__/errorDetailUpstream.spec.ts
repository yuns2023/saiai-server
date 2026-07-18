import { describe, expect, it } from 'vitest'
import { formatAccountLabel, parseEmbeddedUpstreamEvents } from '../errorDetailUpstream'

describe('errorDetailUpstream', () => {
  it('formats an account name first and falls back to a prefixed account id', () => {
    expect(formatAccountLabel(' primary account ', 42)).toBe('primary account')
    expect(formatAccountLabel('', 42)).toBe('#42')
    expect(formatAccountLabel(undefined, null)).toBe('—')
  })

  it('parses account context and native upstream fields from embedded events', () => {
    const [event] = parseEmbeddedUpstreamEvents(JSON.stringify([{
      account_id: 46703,
      account_name: 'Claude OAuth A',
      upstream_status_code: 400,
      upstream_request_id: 'req_01ABC',
      kind: 'http_error',
      message: 'fallbacks: Extra inputs are not permitted'
    }]))

    expect(event).toMatchObject({
      accountId: 46703,
      accountName: 'Claude OAuth A',
      statusCode: 400,
      requestId: 'req_01ABC'
    })
  })

  it('keeps each failover attempt tied to its own account and in original order', () => {
    const events = parseEmbeddedUpstreamEvents(JSON.stringify([
      { account_id: 11, account_name: 'first account', kind: 'failover' },
      { account_id: 22, account_name: 'second account', kind: 'http_error' }
    ]))

    expect(events.map((event) => ({
      accountId: event.accountId,
      accountName: event.accountName,
      kind: event.kind
    }))).toEqual([
      { accountId: 11, accountName: 'first account', kind: 'failover' },
      { accountId: 22, accountName: 'second account', kind: 'http_error' }
    ])
  })

  it('falls back to account context inside a JSON detail payload', () => {
    const [event] = parseEmbeddedUpstreamEvents(JSON.stringify({
      detail: JSON.stringify({
        account_id: 99,
        account_name: 'detail account',
        status_code: 503,
        request_id: 'req_detail',
        reason: 'overloaded'
      })
    }))

    expect(event).toMatchObject({
      accountId: 99,
      accountName: 'detail account',
      statusCode: 503,
      requestId: 'req_detail',
      reason: 'overloaded'
    })
  })

  it('preserves an unparseable payload as an expandable embedded detail', () => {
    expect(parseEmbeddedUpstreamEvents('plain upstream error')).toEqual([{
      key: 'embedded-0',
      kind: '',
      message: '',
      detail: 'plain upstream error',
      reason: '',
      statusCode: null,
      requestId: '',
      accountId: null,
      accountName: ''
    }])
  })
})
