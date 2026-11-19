import { describe, expect, it, vi } from 'vitest'
import { mount } from '@vue/test-utils'
import AccountActionMenu from '../AccountActionMenu.vue'
import type { Account } from '@/types'

vi.mock('vue-i18n', () => ({ useI18n: () => ({ t: (key: string) => key }) }))

const mountMenu = (account: Partial<Account>) =>
  mount(AccountActionMenu, {
    props: { show: true, position: { top: 0, left: 0 }, account: { id: 1, name: 'a', platform: 'anthropic', type: 'setup-token', status: 'active', ...account } as Account },
    global: { stubs: { Teleport: true, Icon: true } }
  })

describe('AccountActionMenu sharing entry', () => {
  it('shows 共享设置 only for accounts with an owner', async () => {
    expect(mountMenu({ owner_user_id: null }).find('[data-testid=menu-market]').exists()).toBe(false)
    const w = mountMenu({ owner_user_id: 7 })
    await w.get('[data-testid=menu-market]').trigger('click')
    expect(w.emitted('market')?.[0]?.[0]).toMatchObject({ id: 1 })
    expect(w.emitted('close')).toBeTruthy()
    // The market is Anthropic only: no entry for an owned OpenAI account.
    expect(mountMenu({ owner_user_id: 7, platform: 'openai', type: 'oauth' }).find('[data-testid=menu-market]').exists()).toBe(false)
  })
})
