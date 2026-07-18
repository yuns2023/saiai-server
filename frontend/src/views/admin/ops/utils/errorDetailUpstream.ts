export type EmbeddedUpstreamEvent = {
  key: string
  kind: string
  message: string
  detail: string
  reason: string
  statusCode: number | null
  requestId: string
  accountId: number | null
  accountName: string
}

export function formatAccountLabel(accountName: unknown, accountId: unknown): string {
  const name = stringValue(accountName)
  if (name) return name

  const id = numberValue(accountId)
  return id == null ? '—' : `#${id}`
}

export function parseEmbeddedUpstreamEvents(rawValue: unknown): EmbeddedUpstreamEvent[] {
  const raw = String(rawValue || '').trim()
  if (!raw || raw === '[]' || raw === '{}' || raw.toLowerCase() === 'null') return []

  try {
    const parsed = JSON.parse(raw)
    const events = Array.isArray(parsed) ? parsed : [parsed]
    return events
      .map((item, idx) => normalizeEmbeddedUpstreamEvent(item, idx))
      .filter((item): item is EmbeddedUpstreamEvent => Boolean(item))
  } catch {
    return [{
      key: 'embedded-0',
      kind: '',
      message: '',
      detail: raw,
      reason: '',
      statusCode: null,
      requestId: '',
      accountId: null,
      accountName: ''
    }]
  }
}

function normalizeEmbeddedUpstreamEvent(item: unknown, idx: number): EmbeddedUpstreamEvent | null {
  if (!item || typeof item !== 'object' || Array.isArray(item)) return null

  const event = item as Record<string, unknown>
  const detailRaw = stringValue(event.detail)
  const detailObject = parseJSONObject(detailRaw)

  return {
    key: `embedded-${idx}`,
    kind: stringValue(event.kind),
    message: stringValue(event.message),
    detail: detailRaw || JSON.stringify(event),
    reason: stringValue(detailObject?.reason) || stringValue(event.reason),
    statusCode: numberValue(event.upstream_status_code) ?? numberValue(event.status_code) ?? numberValue(detailObject?.status_code),
    requestId:
      stringValue(event.upstream_request_id) ||
      stringValue(event.request_id) ||
      stringValue(event.requestId) ||
      stringValue(detailObject?.request_id),
    accountId: numberValue(event.account_id) ?? numberValue(detailObject?.account_id),
    accountName: stringValue(event.account_name) || stringValue(detailObject?.account_name)
  }
}

function parseJSONObject(raw: string): Record<string, unknown> | null {
  if (!raw) return null
  try {
    const parsed = JSON.parse(raw)
    return parsed && typeof parsed === 'object' && !Array.isArray(parsed)
      ? parsed as Record<string, unknown>
      : null
  } catch {
    return null
  }
}

function stringValue(value: unknown): string {
  return typeof value === 'string' ? value.trim() : ''
}

function numberValue(value: unknown): number | null {
  return typeof value === 'number' && Number.isFinite(value) ? value : null
}
