export interface TextSegment {
  text: string
  /** Set when the segment is an http(s) URL. */
  href?: string
}

// Stops at whitespace and at CJK/ASCII closing punctuation so "见 https://x.y/z。" keeps the full stop out.
const URL_RE = /https?:\/\/[^\s<>"'，。；、！？）】」』]+/g
const TRAILING = /[.,;:!?)\]]+$/

/**
 * Splits admin-written plain text into text and link segments. Only http(s) URLs become links;
 * everything is rendered as text nodes by the caller, so no HTML from the setting ever runs.
 */
export function linkify(src: string): TextSegment[] {
  const out: TextSegment[] = []
  let last = 0
  for (const m of src.matchAll(URL_RE)) {
    let url = m[0]
    const trail = url.match(TRAILING)?.[0] ?? ''
    if (trail) url = url.slice(0, -trail.length)
    const start = m.index ?? 0
    if (start > last) out.push({ text: src.slice(last, start) })
    out.push({ text: url, href: url })
    last = start + url.length
  }
  if (last < src.length) out.push({ text: src.slice(last) })
  return out
}
