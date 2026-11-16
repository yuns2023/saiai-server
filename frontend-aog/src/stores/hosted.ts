import { defineStore } from 'pinia'
import { computed, ref } from 'vue'
import { hostedApi, type OwnedAccount } from '@/api'

/**
 * Hosted accounts the signed-in user owns (now or in the past). Loaded once
 * per session by the app shell; the navigation entry shows only when there is
 * at least one. A failed load leaves the list empty, which hides the entry.
 */
export const useHostedStore = defineStore('hosted', () => {
  const accounts = ref<OwnedAccount[]>([])
  const loaded = ref(false)
  const failed = ref(false)
  const hasAny = computed(() => accounts.value.length > 0)

  /** Bumped by reset(); a list fetched for a previous account is discarded. */
  let generation = 0
  let inflight: Promise<OwnedAccount[]> | null = null

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
        }
        return list
      })
      .catch((e: unknown) => {
        if (issuedIn === generation) failed.value = true
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

  /** First load of the session; never rejects. */
  function ensureLoaded(): void {
    if (!loaded.value) load().catch(() => undefined)
  }

  function reset(): void {
    generation++
    inflight = null
    accounts.value = []
    loaded.value = false
    failed.value = false
  }

  return { accounts, loaded, failed, hasAny, load, ensureLoaded, reset }
})
