import { defineStore } from 'pinia'
import { computed, ref } from 'vue'
import { hostedApi, type OwnedAccount } from '@/api'

/** A successful list is trusted this long before a navigation or focus checks again. */
export const HOSTED_TTL_MS = 5 * 60_000
/** After a failed attempt, wait at least this long before trying again. */
export const HOSTED_RETRY_MS = 30_000

/**
 * Hosted accounts the signed-in user owns (now or in the past). The app shell
 * asks for it on mount, on navigation and when the window regains focus;
 * ensureLoaded() only fetches when the list is older than HOSTED_TTL_MS or
 * the last attempt failed (no sooner than HOSTED_RETRY_MS after it), so an
 * account assigned after sign-in shows up without a reload. The navigation
 * entry shows only when there is at least one account; a failed first load
 * leaves the list empty, which hides the entry.
 */
export const useHostedStore = defineStore('hosted', () => {
  const accounts = ref<OwnedAccount[]>([])
  const loaded = ref(false)
  const failed = ref(false)
  const hasAny = computed(() => accounts.value.length > 0)

  /** Bumped by reset(); a list fetched for a previous account is discarded. */
  let generation = 0
  let inflight: Promise<OwnedAccount[]> | null = null
  let lastSuccessAt: number | null = null
  let lastFailureAt: number | null = null

  /** Fetches the list (sharing a request already in flight). Rejects on failure; the list keeps its last value. */
  function load(): Promise<OwnedAccount[]> {
    if (inflight) return inflight
    const issuedIn = generation
    const request = hostedApi
      .listOwnedAccounts()
      .then((list) => {
        if (issuedIn === generation) {
          accounts.value = list
          failed.value = false
          lastSuccessAt = Date.now()
        }
        return list
      })
      .catch((e: unknown) => {
        if (issuedIn === generation) {
          failed.value = true
          lastFailureAt = Date.now()
        }
        throw e
      })
      .finally(() => {
        if (issuedIn === generation) {
          loaded.value = true
          inflight = null
        }
      })
    inflight = request
    return request
  }

  /** Whether a background check is due now. */
  function due(now: number): boolean {
    if (inflight) return false
    if (failed.value) return lastFailureAt === null || now - lastFailureAt >= HOSTED_RETRY_MS
    return lastSuccessAt === null || now - lastSuccessAt >= HOSTED_TTL_MS
  }

  /** Background check: fetches only when due; never rejects. */
  function ensureLoaded(): void {
    if (due(Date.now())) load().catch(() => undefined)
  }

  function reset(): void {
    generation++
    inflight = null
    lastSuccessAt = null
    lastFailureAt = null
    accounts.value = []
    loaded.value = false
    failed.value = false
  }

  return { accounts, loaded, failed, hasAny, load, ensureLoaded, reset }
})
