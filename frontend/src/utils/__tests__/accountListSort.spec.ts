import { describe, expect, it } from 'vitest'
import {
  DEFAULT_ACCOUNT_LIST_SORT,
  normalizeAccountListSort,
  readPersistedAccountListSort
} from '../accountListSort'

const sortable = new Set(['id', 'name', 'status', 'schedulable', 'priority', 'rate_multiplier', 'last_used_at', 'expires_at'])
const storageWith = (value: string | null) => ({ getItem: () => value })

describe('accountListSort', () => {
  it('passes whitelisted sortable keys through', () => {
    expect(normalizeAccountListSort('last_used_at', 'desc', sortable)).toEqual({ sort_by: 'last_used_at', sort_order: 'desc' })
    expect(normalizeAccountListSort('priority', 'asc', sortable)).toEqual({ sort_by: 'priority', sort_order: 'asc' })
    expect(normalizeAccountListSort('id', 'weird', sortable)).toEqual({ sort_by: 'id', sort_order: 'asc' })
  })

  it('falls back to name asc for unknown, hidden or non-server keys', () => {
    expect(normalizeAccountListSort('bogus', 'desc', sortable)).toEqual(DEFAULT_ACCOUNT_LIST_SORT)
    expect(normalizeAccountListSort('expires_at', 'desc', new Set(['id', 'name']))).toEqual(DEFAULT_ACCOUNT_LIST_SORT)
    expect(normalizeAccountListSort('notes', 'desc', new Set(['notes']))).toEqual(DEFAULT_ACCOUNT_LIST_SORT)
    expect(normalizeAccountListSort(undefined, undefined, sortable)).toEqual(DEFAULT_ACCOUNT_LIST_SORT)
  })

  it('reads the DataTable persisted state', () => {
    expect(readPersistedAccountListSort('k', sortable, storageWith('{"key":"expires_at","order":"desc"}'))).toEqual({
      sort_by: 'expires_at',
      sort_order: 'desc'
    })
    expect(readPersistedAccountListSort('k', sortable, storageWith(null))).toEqual(DEFAULT_ACCOUNT_LIST_SORT)
    expect(readPersistedAccountListSort('k', sortable, storageWith('not json'))).toEqual(DEFAULT_ACCOUNT_LIST_SORT)
    expect(readPersistedAccountListSort('k', sortable, storageWith('null'))).toEqual(DEFAULT_ACCOUNT_LIST_SORT)
  })
})
