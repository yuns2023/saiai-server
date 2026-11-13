import { defineStore } from 'pinia'
import { computed, ref } from 'vue'
import { announcementsApi } from '@/api'
import type { UserAnnouncement } from '@/types'

export const useNoticesStore = defineStore('notices', () => {
  const items = ref<UserAnnouncement[]>([])
  const loaded = ref(false)

  const unread = computed(() => items.value.filter((a) => !a.read_at))
  /** Unread popup-mode notices are surfaced as a banner on every page. */
  const banner = computed(() => unread.value.find((a) => a.notify_mode === 'popup') ?? null)

  /** Bumped by reset(); a list fetched for a previous account is discarded. */
  let generation = 0

  async function load(): Promise<void> {
    const issuedIn = generation
    try {
      const list = await announcementsApi.list()
      if (issuedIn === generation) items.value = list
    } finally {
      if (issuedIn === generation) loaded.value = true
    }
  }

  function reset(): void {
    generation++
    items.value = []
    loaded.value = false
  }

  async function markRead(id: number): Promise<void> {
    const now = new Date().toISOString()
    items.value = items.value.map((a) => (a.id === id ? { ...a, read_at: a.read_at ?? now } : a))
    try {
      await announcementsApi.markRead(id)
    } catch {
      // Read state is cosmetic; a failed write just shows it again next load.
    }
  }

  return { items, loaded, unread, banner, load, markRead, reset }
})
