/**
 * AOG user UI handoff.
 *
 * When the backend runs with SERVER_USER_UI=aog it injects
 * window.__SAIAI_USER_UI__ = "aog". This classic SPA then only owns the admin
 * console (/admin, /admin/*) and the setup wizard entry (/setup); every other
 * page (login, customer pages, 404) belongs to the AOG SPA and must be loaded
 * with a full page navigation, because an in-SPA push would render the old
 * customer page instead.
 *
 * Keep isClassicOwnedPath in sync with isClassicOwnedPage in
 * backend/internal/web/ui_routing.go.
 */

export function isAOGUserUI(): boolean {
  return typeof window !== 'undefined' && window.__SAIAI_USER_UI__ === 'aog'
}

export function isClassicOwnedPath(path: string): boolean {
  return path === '/admin' || path.startsWith('/admin/') || path === '/setup'
}

/**
 * Returns true when navigation to `path` must leave this SPA.
 * A target equal to the URL the browser already shows is never handed off:
 * the server already decided this SPA serves it, and reloading would loop.
 * Guard redirects (e.g. /admin -> /login) are handed off even on first load.
 */
export function shouldHandOffToAOG(path: string, currentPath: string): boolean {
  if (!isAOGUserUI()) return false
  if (path === currentPath) return false
  return !isClassicOwnedPath(path)
}
