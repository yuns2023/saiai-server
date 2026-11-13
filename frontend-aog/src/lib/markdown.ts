import { marked } from 'marked'
import DOMPurify from 'dompurify'

marked.setOptions({ gfm: true, breaks: true })

/** Admin-authored announcement markdown → sanitized HTML. */
export function renderMarkdown(src: string): string {
  return DOMPurify.sanitize(marked.parse(src ?? '', { async: false }) as string)
}
