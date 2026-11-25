/**
 * Common component types
 */

export interface Column {
  key: string
  label: string
  sortable?: boolean
  /**
   * Several sortable fields shown in one merged column: the header renders
   * each label as its own sort toggle (the column itself is not sortable).
   */
  sortOptions?: Array<{ key: string; label: string }>
  class?: string
  formatter?: (value: any, row: any) => string
}
