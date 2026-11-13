/**
 * Page ownership and post-login redirects.
 *
 * The classic SPA keeps the admin console (/admin, /admin/*) and the setup
 * wizard entry (/setup); every other page belongs to this app. Keep
 * isClassicOwnedPath in sync with backend/internal/web/ui_routing.go and
 * frontend/src/router/userUiHandoff.ts.
 */

export function isClassicOwnedPath(path: string): boolean {
  return path === '/admin' || path.startsWith('/admin/') || path === '/setup'
}

/**
 * Accepts only same-origin, path-absolute targets. Returns the normalized
 * path + query + hash, or null for anything that could leave the site
 * ("//host", "/\\host", "https://…", "javascript:…", encoded variants).
 */
export function safeRedirectTarget(raw: unknown, origin = window.location.origin): string | null {
  if (typeof raw !== 'string') return null
  const value = raw.trim()
  if (!value.startsWith('/') || value.startsWith('//') || value.startsWith('/\\')) return null
  if (/[\u0000-\u001f\u007f]/.test(value)) return null
  try {
    const url = new URL(value, origin)
    if (url.origin !== origin) return null
    return `${url.pathname}${url.search}${url.hash}`
  } catch {
    return null
  }
}

/** Where a signed-in user should land after login. */
export function postLoginTarget(rawRedirect: unknown, isAdmin: boolean): { path: string; fullPage: boolean } {
  const target = safeRedirectTarget(rawRedirect)
  if (target) {
    const pathname = decodedPath(target.split(/[?#]/, 1)[0])
    if (pathname === null) return isAdmin ? { path: '/admin/dashboard', fullPage: true } : { path: '/dashboard', fullPage: false }
    if (isClassicOwnedPath(pathname)) {
      // Non-admins cannot use the console; send them home instead of into a guard loop.
      return isAdmin ? { path: target, fullPage: true } : { path: '/dashboard', fullPage: false }
    }
    if (!['/login', '/register'].includes(pathname)) {
      return { path: target, fullPage: false }
    }
  }
  return isAdmin ? { path: '/admin/dashboard', fullPage: true } : { path: '/dashboard', fullPage: false }
}

/**
 * The server decides ownership on the once-decoded path (Go's URL.Path), so
 * "/%61dmin/users" belongs to the console there. Mirror that single decode;
 * a path that does not decode cleanly is treated as unusable.
 */
function decodedPath(rawPath: string): string | null {
  try {
    const decoded = decodeURIComponent(rawPath)
    if (/[\u0000-\u001f\u007f\\]/.test(decoded)) return null
    // The server applies path.Clean after decoding; anything Clean would
    // change ("//", "/./", "/../", trailing dot segments) is rejected here
    // rather than guessed at.
    if (/\/\/|\/\.{1,2}(\/|$)/.test(decoded)) return null
    return decoded
  } catch {
    return null
  }
}
