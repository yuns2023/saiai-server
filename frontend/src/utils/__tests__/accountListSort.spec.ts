import { describe, expect, it } from 'vitest'
import {
  DEFAULT_ACCOUNT_LIST_SORT,
  normalizeAccountListSort,
  readPersistedAccountListSort
} from '../accountListSort'

const storageWith = (value: string | null) => ({ getItem: () => value })

describe('accountListSort', () => {
  it('passes whitelisted sortable keys through', () => {
    expect(normalizeAccountListSort('last_used_at', 'desc')).toEqual({ sort_by: 'last_used_at', sort_order: 'desc' })
    expect(normalizeAccountListSort('priority', 'asc')).toEqual({ sort_by: 'priority', sort_order: 'asc' })
    expect(normalizeAccountListSort('id', 'weird')).toEqual({ sort_by: 'id', sort_order: 'asc' })
  })

  it('falls back to name asc for unknown or non-server keys', () => {
    expect(normalizeAccountListSort('bogus', 'desc')).toEqual(DEFAULT_ACCOUNT_LIST_SORT)
    expect(normalizeAccountListSort('notes', 'desc')).toEqual(DEFAULT_ACCOUNT_LIST_SORT)
    expect(normalizeAccountListSort(undefined, undefined)).toEqual(DEFAULT_ACCOUNT_LIST_SORT)
  })

  it('reads the DataTable persisted state', () => {
    expect(readPersistedAccountListSort('k', storageWith('{"key":"expires_at","order":"desc"}'))).toEqual({
      sort_by: 'expires_at',
      sort_order: 'desc'
    })
    expect(readPersistedAccountListSort('k', storageWith(null))).toEqual(DEFAULT_ACCOUNT_LIST_SORT)
    expect(readPersistedAccountListSort('k', storageWith('not json'))).toEqual(DEFAULT_ACCOUNT_LIST_SORT)
    expect(readPersistedAccountListSort('k', storageWith('null'))).toEqual(DEFAULT_ACCOUNT_LIST_SORT)
  })
})
