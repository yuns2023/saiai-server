// Server-side sort params for the admin accounts list (GET /admin/accounts
// sort_by / sort_order). The backend whitelist lives in
// backend/internal/service/account_list_sort.go; unknown keys fall back to
// id DESC there, so keep the two lists in sync.

export type AccountSortOrder = 'asc' | 'desc'

export interface AccountListSortParams {
  sort_by: string
  sort_order: AccountSortOrder
}

export const ACCOUNT_SERVER_SORT_KEYS: ReadonlySet<string> = new Set([
  'id',
  'name',
  'status',
  'schedulable',
  'priority',
  'rate_multiplier',
  'last_used_at',
  'expires_at'
])

export const DEFAULT_ACCOUNT_LIST_SORT: AccountListSortParams = { sort_by: 'name', sort_order: 'asc' }

const normalizeOrder = (order: unknown): AccountSortOrder => (order === 'desc' ? 'desc' : 'asc')

/**
 * Normalizes a sort key/order against the currently sortable columns, the
 * same way DataTable does: an unknown or non-sortable key falls back to the
 * default (name asc) so the request always matches the header arrow.
 */
export function normalizeAccountListSort(
  key: unknown,
  order: unknown,
  sortableKeys: ReadonlySet<string>
): AccountListSortParams {
  if (typeof key === 'string' && sortableKeys.has(key) && ACCOUNT_SERVER_SORT_KEYS.has(key)) {
    return { sort_by: key, sort_order: normalizeOrder(order) }
  }
  return { ...DEFAULT_ACCOUNT_LIST_SORT }
}

/**
 * Reads the sort DataTable persisted under storageKey ({ key, order }) so the
 * first list request uses the order the header shows.
 */
export function readPersistedAccountListSort(
  storageKey: string,
  sortableKeys: ReadonlySet<string>,
  storage: Pick<Storage, 'getItem'> | undefined = typeof localStorage === 'undefined' ? undefined : localStorage
): AccountListSortParams {
  try {
    const raw = storage?.getItem(storageKey)
    if (raw) {
      const parsed = JSON.parse(raw) as { key?: unknown; order?: unknown } | null
      if (parsed && typeof parsed === 'object') {
        return normalizeAccountListSort(parsed.key, parsed.order, sortableKeys)
      }
    }
  } catch {
    // Corrupt storage: fall through to the default, as DataTable does.
  }
  return { ...DEFAULT_ACCOUNT_LIST_SORT }
}
