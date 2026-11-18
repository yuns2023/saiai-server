import { defineStore } from 'pinia'
import { computed, ref } from 'vue'
import { hostedApi, type OwnedAccount, type OwnerState, type SelfOnboardConfig } from '@/api'

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
 *
 * The self-onboarding switch is checked the same way (own timestamps): when it
 * is on, the entry shows for everyone so a user can submit a first account.
 * A server without self-onboarding (or a failed check) counts as off.
 */
export const useHostedStore = defineStore('hosted', () => {
  const accounts = ref<OwnedAccount[]>([])
  const loaded = ref(false)
  const failed = ref(false)
  const hasAny = computed(() => accounts.value.length > 0)
  const onboard = ref<SelfOnboardConfig | null>(null)
  /** An owner action (pause / resume / withdraw) in flight, kept across page remounts so two never overlap. */
  const ownerBusy = ref<number | null>(null)
  /** I have at least one self-onboarding application (any status); null = unknown. */
  const hasApplications = ref<boolean | null>(null)
  /**
   * The /hosted entry: owners, anyone while self-onboarding is open, and anyone
   * with an application (switch off: they can still see and cancel it).
   */
  const showEntry = computed(() => hasAny.value || onboard.value?.enabled === true || hasApplications.value === true)

  /** Bumped by reset(); a list fetched for a previous account is discarded. */
  let generation = 0
  /** Owner actions applied so far (ordering against list reads). */
  let ownerSeq = 0
  const ownerOverrides = new Map<number, { seq: number; state: OwnerState }>()
  let inflight: Promise<OwnedAccount[]> | null = null
  let lastSuccessAt: number | null = null
  let lastFailureAt: number | null = null

  /** Fetches the list (sharing a request already in flight). Rejects on failure; the list keeps its last value. */
  function load(): Promise<OwnedAccount[]> {
    if (inflight) return inflight
    const issuedIn = generation
    const issuedAfter = ownerSeq
    const request = hostedApi
      .listOwnedAccounts()
      .then((raw) => {
        // A pause / resume / withdraw that finished after this list was asked for wins over it.
        const list = raw.map((a) => {
          const o = ownerOverrides.get(a.account_id)
          return o && o.seq > issuedAfter ? { ...a, owner_paused: o.state.owner_paused, withdraw_requested: o.state.withdraw_requested } : a
        })
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

  /**
   * A list read that starts after now: a request already in flight (possibly
   * sent before a write the caller just made) is awaited, then a new one goes out.
   */
  async function reload(): Promise<OwnedAccount[]> {
    const issuedIn = generation
    if (inflight) await inflight.catch(() => undefined)
    if (issuedIn !== generation) return accounts.value
    // Another fresh caller may have started one meanwhile; it also began after our write.
    return load()
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

  /* ---------- Self-onboarding switch ---------- */
  let configInflight: Promise<SelfOnboardConfig> | null = null
  let configSuccessAt: number | null = null
  let configFailureAt: number | null = null

  /** Fetches the self-onboarding config (sharing a request in flight). Rejects on failure; the last value stays. */
  function loadConfig(): Promise<SelfOnboardConfig> {
    if (configInflight) return configInflight
    const issuedIn = generation
    // Wrapped so a missing endpoint or a throwing stub also lands in catch.
    const request = Promise.resolve()
      .then(() => hostedApi.selfOnboardConfig())
      .then((cfg) => {
        if (issuedIn === generation) {
          onboard.value = cfg
          configSuccessAt = Date.now()
          configFailureAt = null
          // Best effort: whether there is anything to show on /hosted besides the form.
          Promise.resolve()
            .then(() => hostedApi.listApplications())
            .then((list) => {
              if (issuedIn === generation) hasApplications.value = list.length > 0
            })
            .catch(() => undefined)
        }
        return cfg
      })
      .catch((e: unknown) => {
        if (issuedIn === generation) configFailureAt = Date.now()
        throw e
      })
      .finally(() => {
        if (issuedIn === generation) configInflight = null
      })
    configInflight = request
    return request
  }

  function configDue(now: number): boolean {
    if (configInflight) return false
    if (configFailureAt !== null) return now - configFailureAt >= HOSTED_RETRY_MS
    return configSuccessAt === null || now - configSuccessAt >= HOSTED_TTL_MS
  }

  /** Background check of the switch; never rejects. */
  function ensureConfig(): void {
    if (configDue(Date.now())) loadConfig().catch(() => undefined)
  }

  /** Current session tag; pass it back to applyOwnerState so a late answer after sign-out is dropped. */
  function session(): number {
    return generation
  }

  /** Applies an owner pause / resume / withdraw answer to the listed account; false when the session changed meanwhile. */
  function applyOwnerState(state: OwnerState, issuedIn: number = generation): boolean {
    if (issuedIn !== generation) return false
    ownerOverrides.set(state.id, { seq: ++ownerSeq, state })
    accounts.value = accounts.value.map((a) =>
      a.account_id === state.id ? { ...a, owner_paused: state.owner_paused, withdraw_requested: state.withdraw_requested } : a
    )
    return true
  }

  function reset(): void {
    generation++
    inflight = null
    lastSuccessAt = null
    lastFailureAt = null
    accounts.value = []
    loaded.value = false
    failed.value = false
    configInflight = null
    configSuccessAt = null
    configFailureAt = null
    onboard.value = null
    hasApplications.value = null
    ownerOverrides.clear()
    ownerBusy.value = null
  }

  return { accounts, loaded, failed, hasAny, onboard, hasApplications, showEntry, ownerBusy, load, reload, ensureLoaded, loadConfig, ensureConfig, session, applyOwnerState, reset }
})
