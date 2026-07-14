/**
 * Encode one RFC 4180-style CSV cell and neutralize spreadsheet formulas.
 * Always quoting cells also keeps commas, quotes, and line breaks lossless.
 */
export function escapeCsvCell(value: unknown): string {
  const raw = value == null ? '' : String(value)
  const spreadsheetSafe = /^(?:[\t\r\n]|\s*[=+\-@])/.test(raw) ? `'${raw}` : raw
  return `"${spreadsheetSafe.replace(/"/g, '""')}"`
}
