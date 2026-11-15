/**
 * Which upstream platforms this customer UI offers.
 *
 * The site is a Claude Code entry point: groups of any other platform are
 * dropped from every list and picker here, and no platform name is shown
 * to users. Change this one list if the site ever offers more.
 */
export const ALLOWED_PLATFORMS: readonly string[] = ['anthropic']

export function isAllowedPlatform(platform: string | null | undefined): boolean {
  return !!platform && ALLOWED_PLATFORMS.includes(platform)
}

export function allowedGroups<T extends { platform?: string | null }>(groups: readonly T[]): T[] {
  return groups.filter((g) => isAllowedPlatform(g.platform))
}
