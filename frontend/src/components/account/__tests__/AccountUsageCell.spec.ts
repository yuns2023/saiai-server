import { describe, expect, it, vi, beforeEach } from 'vitest'
import { flushPromises, mount } from '@vue/test-utils'
import AccountUsageCell from '../AccountUsageCell.vue'
import type { Account } from '@/types'
import { clearAccountUsageCache } from '@/utils/accountUsageCache'

const { getUsage } = vi.hoisted(() => ({
  getUsage: vi.fn()
}))

vi.mock('@/api/admin', () => ({
  adminAPI: {
    accounts: {
      getUsage
    }
  }
}))

vi.mock('vue-i18n', async () => {
  const actual = await vi.importActual<typeof import('vue-i18n')>('vue-i18n')
  return {
    ...actual,
    useI18n: () => ({
      t: (key: string) => key
    })
  }
})

function makeAccount(overrides: Partial<Account>): Account {
  return {
    id: 1,
    name: 'account',
    platform: 'antigravity',
    type: 'oauth',
    proxy_id: null,
    concurrency: 1,
    priority: 1,
    status: 'active',
    error_message: null,
    last_used_at: null,
    expires_at: null,
    auto_pause_on_expired: true,
    created_at: '2026-03-15T00:00:00Z',
    updated_at: '2026-03-15T00:00:00Z',
    schedulable: true,
    rate_limited_at: null,
    rate_limit_reset_at: null,
    overload_until: null,
    temp_unschedulable_until: null,
    temp_unschedulable_reason: null,
    session_window_start: null,
    session_window_end: null,
    session_window_status: null,
    ...overrides,
  }
}

describe('AccountUsageCell', () => {
  beforeEach(() => {
    getUsage.mockReset()
    clearAccountUsageCache()
  })

  const anthropicUsage = {
    five_hour: { utilization: 16, resets_at: '2099-03-07T12:00:00Z', remaining_seconds: 3600 },
    source: 'passive'
  }
  const barStub = {
    UsageProgressBar: {
      props: ['label', 'utilization'],
      template: '<div class="usage-bar">{{ label }}|{{ utilization }}</div>'
    },
    AccountQuotaInfo: true
  }

  it.each(['inactive', 'error'] as const)('%s 状态的账号不自动查询，点击查询后才请求', async (status) => {
    getUsage.mockResolvedValue(anthropicUsage)
    const wrapper = mount(AccountUsageCell, {
      props: { account: makeAccount({ id: 6001, platform: 'anthropic', type: 'oauth', status }) },
      global: { stubs: barStub }
    })
    await flushPromises()
    expect(getUsage).not.toHaveBeenCalled()
    expect(wrapper.find('[data-testid="usage-deferred"]').exists()).toBe(true)

    // Page refresh does not query a skipped account either.
    await wrapper.setProps({ manualRefreshToken: 1 })
    await flushPromises()
    expect(getUsage).not.toHaveBeenCalled()

    await wrapper.find('[data-testid="usage-deferred"] button').trigger('click')
    await flushPromises()
    expect(getUsage).toHaveBeenCalledTimes(1)
    expect(getUsage).toHaveBeenCalledWith(6001, 'passive')
    expect(wrapper.text()).toContain('5h|16')
  })

  it('状态从停用变回启用会自动查询；变成错误后回到“-”，刷新也不再查', async () => {
    getUsage.mockResolvedValue(anthropicUsage)
    const wrapper = mount(AccountUsageCell, {
      props: { account: makeAccount({ id: 6101, platform: 'anthropic', type: 'oauth', status: 'inactive' }), manualRefreshToken: 0 },
      global: { stubs: barStub }
    })
    await flushPromises()
    expect(getUsage).not.toHaveBeenCalled()

    await wrapper.setProps({ account: makeAccount({ id: 6101, platform: 'anthropic', type: 'oauth', status: 'active' }) })
    await flushPromises()
    expect(getUsage).toHaveBeenCalledTimes(1)
    expect(wrapper.text()).toContain('5h|16')

    await wrapper.setProps({ account: makeAccount({ id: 6101, platform: 'anthropic', type: 'oauth', status: 'error' }) })
    await flushPromises()
    expect(wrapper.find('[data-testid="usage-deferred"]').exists()).toBe(true)
    await wrapper.setProps({ manualRefreshToken: 1 })
    await flushPromises()
    expect(getUsage).toHaveBeenCalledTimes(1)
  })

  it('手动查询过的错误账号会跟随页面刷新', async () => {
    getUsage.mockResolvedValue(anthropicUsage)
    const wrapper = mount(AccountUsageCell, {
      props: { account: makeAccount({ id: 6102, platform: 'anthropic', type: 'oauth', status: 'error' }), manualRefreshToken: 0 },
      global: { stubs: barStub }
    })
    await flushPromises()
    await wrapper.find('[data-testid="usage-deferred"] button').trigger('click')
    await flushPromises()
    await wrapper.setProps({ manualRefreshToken: 1 })
    await flushPromises()
    expect(getUsage).toHaveBeenCalledTimes(2)
  })

  it('状态变化后，之前发出的请求晚到也不会写回', async () => {
    let resolve!: (v: unknown) => void
    getUsage.mockReturnValueOnce(new Promise((r) => { resolve = r }))
    const wrapper = mount(AccountUsageCell, {
      props: { account: makeAccount({ id: 6105, platform: 'anthropic', type: 'oauth' }) },
      global: { stubs: barStub }
    })
    await wrapper.setProps({ account: makeAccount({ id: 6105, platform: 'anthropic', type: 'oauth', status: 'error' }) })
    resolve(anthropicUsage)
    await flushPromises()
    expect(wrapper.find('[data-testid="usage-deferred"]').exists()).toBe(true)
    expect(wrapper.text()).not.toContain('5h|16')
  })

  it('手动查询过的停用账号重新挂载后仍显示结果', async () => {
    getUsage.mockResolvedValue(anthropicUsage)
    const account = makeAccount({ id: 6106, platform: 'anthropic', type: 'oauth', status: 'inactive' })
    const first = mount(AccountUsageCell, { props: { account }, global: { stubs: barStub } })
    await flushPromises()
    await first.find('[data-testid="usage-deferred"] button').trigger('click')
    await flushPromises()
    first.unmount()
    const second = mount(AccountUsageCell, { props: { account }, global: { stubs: barStub } })
    await flushPromises()
    expect(getUsage).toHaveBeenCalledTimes(1)
    expect(second.text()).toContain('5h|16')
  })

  it('手动查询过的错误账号，页面刷新时在视野外，滚回后仍会查询', async () => {
    getUsage.mockResolvedValue(anthropicUsage)
    const account = makeAccount({ id: 6108, platform: 'anthropic', type: 'oauth', status: 'error' })
    const first = mount(AccountUsageCell, { props: { account, manualRefreshToken: 0 }, global: { stubs: barStub } })
    await flushPromises()
    await first.find('[data-testid="usage-deferred"] button').trigger('click')
    await flushPromises()
    first.unmount()
    // page refreshed (token 1) while the row was off-screen
    const second = mount(AccountUsageCell, { props: { account, manualRefreshToken: 1 }, global: { stubs: barStub } })
    await flushPromises()
    expect(getUsage).toHaveBeenCalledTimes(2)
    expect(second.text()).toContain('5h|16')
    await second.setProps({ manualRefreshToken: 2 })
    await flushPromises()
    expect(getUsage).toHaveBeenCalledTimes(3)
  })

  it('自动查过的账号在视野外变成错误后，重新挂载回到“-”且刷新不再查', async () => {
    getUsage.mockResolvedValue(anthropicUsage)
    const first = mount(AccountUsageCell, {
      props: { account: makeAccount({ id: 6107, platform: 'anthropic', type: 'oauth' }), manualRefreshToken: 0 },
      global: { stubs: barStub }
    })
    await flushPromises()
    first.unmount()
    const second = mount(AccountUsageCell, {
      props: { account: makeAccount({ id: 6107, platform: 'anthropic', type: 'oauth', status: 'error' }), manualRefreshToken: 0 },
      global: { stubs: barStub }
    })
    await flushPromises()
    expect(second.find('[data-testid="usage-deferred"]').exists()).toBe(true)
    await second.setProps({ manualRefreshToken: 1 })
    await flushPromises()
    expect(getUsage).toHaveBeenCalledTimes(1)
  })

  it('刷新失败时保留上一次的结果', async () => {
    getUsage.mockResolvedValueOnce(anthropicUsage).mockRejectedValueOnce({ status: 500 })
    const wrapper = mount(AccountUsageCell, {
      props: { account: makeAccount({ id: 6103, platform: 'anthropic', type: 'oauth' }), manualRefreshToken: 0 },
      global: { stubs: barStub }
    })
    await flushPromises()
    await wrapper.setProps({ manualRefreshToken: 1 })
    await flushPromises()
    expect(getUsage).toHaveBeenCalledTimes(2)
    expect(wrapper.text()).toContain('5h|16')
    expect(wrapper.text()).not.toContain('common.error')
  })

  it('账号类型变化后不复用旧缓存', async () => {
    getUsage.mockResolvedValue(anthropicUsage)
    const first = mount(AccountUsageCell, {
      props: { account: makeAccount({ id: 6104, platform: 'anthropic', type: 'oauth' }) },
      global: { stubs: barStub }
    })
    await flushPromises()
    first.unmount()
    mount(AccountUsageCell, {
      props: { account: makeAccount({ id: 6104, platform: 'anthropic', type: 'setup-token' }) },
      global: { stubs: barStub }
    })
    await flushPromises()
    expect(getUsage).toHaveBeenCalledTimes(2)
  })

  it('重新挂载（滚动/列表重载）复用缓存，不重复请求', async () => {
    getUsage.mockResolvedValue(anthropicUsage)
    const account = makeAccount({ id: 6010, platform: 'anthropic', type: 'oauth' })
    const first = mount(AccountUsageCell, { props: { account }, global: { stubs: barStub } })
    await flushPromises()
    first.unmount()
    const second = mount(AccountUsageCell, { props: { account }, global: { stubs: barStub } })
    await flushPromises()
    expect(getUsage).toHaveBeenCalledTimes(1)
    expect(second.text()).toContain('5h|16')
  })

  it('同时挂载的同一账号共享一个请求', async () => {
    let resolve!: (v: unknown) => void
    getUsage.mockReturnValue(new Promise((r) => { resolve = r }))
    const account = makeAccount({ id: 6020, platform: 'anthropic', type: 'oauth' })
    const a = mount(AccountUsageCell, { props: { account }, global: { stubs: barStub } })
    const b = mount(AccountUsageCell, { props: { account }, global: { stubs: barStub } })
    resolve(anthropicUsage)
    await flushPromises()
    expect(getUsage).toHaveBeenCalledTimes(1)
    expect(a.text()).toContain('5h|16')
    expect(b.text()).toContain('5h|16')
  })

  it('页面刷新：重载期间重新挂载用缓存，刷新信号只再请求一次；之后挂载的行不复用刷新前的缓存', async () => {
    getUsage.mockResolvedValue(anthropicUsage)
    const account = makeAccount({ id: 6030, platform: 'anthropic', type: 'oauth' })
    const other = makeAccount({ id: 6031, platform: 'anthropic', type: 'oauth' })
    const first = mount(AccountUsageCell, { props: { account, manualRefreshToken: 0 }, global: { stubs: barStub } })
    const offscreen = mount(AccountUsageCell, { props: { account: other, manualRefreshToken: 0 }, global: { stubs: barStub } })
    await flushPromises()
    expect(getUsage).toHaveBeenCalledTimes(2)
    first.unmount()
    offscreen.unmount()

    // list reload remounts the row before the refresh token is bumped
    const remounted = mount(AccountUsageCell, { props: { account, manualRefreshToken: 0 }, global: { stubs: barStub } })
    await flushPromises()
    expect(getUsage).toHaveBeenCalledTimes(2)
    await remounted.setProps({ manualRefreshToken: 1 })
    await flushPromises()
    expect(getUsage).toHaveBeenCalledTimes(3)

    // a row scrolled into view after the refresh must not show pre-refresh data
    mount(AccountUsageCell, { props: { account: other, manualRefreshToken: 1 }, global: { stubs: barStub } })
    await flushPromises()
    expect(getUsage).toHaveBeenCalledTimes(4)
    expect(getUsage).toHaveBeenLastCalledWith(6031, 'passive')
  })

  it('Anthropic 账户会显示独立的 Fable 周额度', async () => {
    getUsage.mockResolvedValue({
      source: 'passive',
      five_hour: null,
      seven_day: null,
      seven_day_sonnet: null,
      seven_day_fable: {
        utilization: 43,
        resets_at: '2026-08-14T12:00:00Z',
        remaining_seconds: 518400
      }
    })

    const wrapper = mount(AccountUsageCell, {
      props: {
        account: makeAccount({
          id: 5001,
          platform: 'anthropic',
          type: 'oauth',
          extra: {}
        })
      },
      global: {
        stubs: {
          UsageProgressBar: {
            props: ['label', 'utilization', 'resetsAt', 'tooltip', 'color'],
            template: '<div class="usage-bar">{{ label }}|{{ utilization }}|{{ resetsAt }}|{{ tooltip }}|{{ color }}</div>'
          },
          AccountQuotaInfo: true
        }
      }
    })

    await flushPromises()

    expect(getUsage).toHaveBeenCalledWith(5001, 'passive')
    expect(wrapper.text()).toContain(
      '7d F|43|2026-08-14T12:00:00Z|admin.accounts.usageWindow.fableWeekly|amber',
    )
  })

  it('退休的 Antigravity 账号不会触发用量刷新', async () => {
    mount(AccountUsageCell, {
      props: {
        account: makeAccount({
          id: 1001,
          platform: 'antigravity',
          type: 'oauth',
          extra: {}
        })
      },
      global: {
        stubs: {
          UsageProgressBar: true,
          AccountQuotaInfo: true
        }
      }
    })

    await flushPromises()

    expect(getUsage).not.toHaveBeenCalled()
  })

  it('OpenAI OAuth 快照已过期时首屏会重新请求 usage', async () => {
    getUsage.mockResolvedValue({
      five_hour: {
        utilization: 15,
        resets_at: '2026-03-08T12:00:00Z',
        remaining_seconds: 3600,
        window_stats: {
          requests: 3,
          tokens: 300,
          cost: 0.03,
          standard_cost: 0.03,
          user_cost: 0.03
        }
      },
      seven_day: {
        utilization: 77,
        resets_at: '2026-03-13T12:00:00Z',
        remaining_seconds: 3600,
        window_stats: {
          requests: 3,
          tokens: 300,
          cost: 0.03,
          standard_cost: 0.03,
          user_cost: 0.03
        }
      }
    })

    const wrapper = mount(AccountUsageCell, {
      props: {
        account: makeAccount({
          id: 2000,
          platform: 'openai',
          type: 'oauth',
          extra: {
            codex_usage_updated_at: '2026-03-07T00:00:00Z',
            codex_5h_used_percent: 12,
            codex_5h_reset_at: '2026-03-08T12:00:00Z',
            codex_7d_used_percent: 34,
            codex_7d_reset_at: '2026-03-13T12:00:00Z'
          }
        })
      },
      global: {
        stubs: {
          UsageProgressBar: {
            props: ['label', 'utilization', 'resetsAt', 'windowStats', 'color'],
            template: '<div class="usage-bar">{{ label }}|{{ utilization }}|{{ windowStats?.tokens }}</div>'
          },
          AccountQuotaInfo: true
        }
      }
    })

    await flushPromises()

    expect(getUsage).toHaveBeenCalledWith(2000)
    expect(wrapper.text()).toContain('5h|15|300')
    expect(wrapper.text()).toContain('7d|77|300')
  })

  it('OpenAI OAuth 有 codex 快照时仍然使用 /usage API 数据渲染', async () => {
    getUsage.mockResolvedValue({
      five_hour: {
        utilization: 18,
        resets_at: '2099-03-07T12:00:00Z',
        remaining_seconds: 3600,
        window_stats: {
          requests: 9,
          tokens: 900,
          cost: 0.09,
          standard_cost: 0.09,
          user_cost: 0.09
        }
      },
      seven_day: {
        utilization: 36,
        resets_at: '2099-03-13T12:00:00Z',
        remaining_seconds: 3600,
        window_stats: {
          requests: 9,
          tokens: 900,
          cost: 0.09,
          standard_cost: 0.09,
          user_cost: 0.09
        }
      }
    })

    const wrapper = mount(AccountUsageCell, {
      props: {
        account: makeAccount({
          id: 2001,
          platform: 'openai',
          type: 'oauth',
          extra: {
            codex_usage_updated_at: '2099-03-07T10:00:00Z',
            codex_5h_used_percent: 12,
            codex_5h_reset_at: '2099-03-07T12:00:00Z',
            codex_7d_used_percent: 34,
            codex_7d_reset_at: '2099-03-13T12:00:00Z'
          }
        })
      },
      global: {
        stubs: {
          UsageProgressBar: {
            props: ['label', 'utilization', 'resetsAt', 'windowStats', 'color'],
            template: '<div class="usage-bar">{{ label }}|{{ utilization }}|{{ windowStats?.tokens }}</div>'
          },
          AccountQuotaInfo: true
        }
      }
    })

    await flushPromises()

    expect(getUsage).toHaveBeenCalledWith(2001)
    // 单一数据源：始终使用 /usage API 返回值，忽略 codex 快照
    expect(wrapper.text()).toContain('5h|18|900')
    expect(wrapper.text()).toContain('7d|36|900')
  })

  it('OpenAI OAuth 有现成快照时，手动刷新信号会触发 usage 重拉', async () => {
    getUsage.mockResolvedValue({
      five_hour: {
        utilization: 18,
        resets_at: '2099-03-07T12:00:00Z',
        remaining_seconds: 3600,
        window_stats: {
          requests: 9,
          tokens: 900,
          cost: 0.09,
          standard_cost: 0.09,
          user_cost: 0.09
        }
      },
      seven_day: {
        utilization: 36,
        resets_at: '2099-03-13T12:00:00Z',
        remaining_seconds: 3600,
        window_stats: {
          requests: 9,
          tokens: 900,
          cost: 0.09,
          standard_cost: 0.09,
          user_cost: 0.09
        }
      }
    })

    const wrapper = mount(AccountUsageCell, {
      props: {
        account: makeAccount({
          id: 2010,
          platform: 'openai',
          type: 'oauth',
          extra: {
            codex_usage_updated_at: '2099-03-07T10:00:00Z',
            codex_5h_used_percent: 12,
            codex_5h_reset_at: '2099-03-07T12:00:00Z',
            codex_7d_used_percent: 34,
            codex_7d_reset_at: '2099-03-13T12:00:00Z'
          },
          rate_limit_reset_at: null
        }),
        manualRefreshToken: 0
      },
      global: {
        stubs: {
          UsageProgressBar: {
            props: ['label', 'utilization', 'resetsAt', 'windowStats', 'color'],
            template: '<div class="usage-bar">{{ label }}|{{ utilization }}|{{ windowStats?.tokens }}</div>'
          },
          AccountQuotaInfo: true
        }
      }
    })

    await flushPromises()
    // mount 时已经拉取一次
    expect(getUsage).toHaveBeenCalledTimes(1)

    await wrapper.setProps({ manualRefreshToken: 1 })
    await flushPromises()

    // 手动刷新再拉一次
    expect(getUsage).toHaveBeenCalledTimes(2)
    expect(getUsage).toHaveBeenCalledWith(2010)
    // 单一数据源：始终使用 /usage API 值
    expect(wrapper.text()).toContain('5h|18|900')
  })

  it('OpenAI OAuth 在无 codex 快照时会回退显示 usage 接口窗口', async () => {
	getUsage.mockResolvedValue({
	  five_hour: {
	    utilization: 0,
	    resets_at: null,
	    remaining_seconds: 0,
	    window_stats: {
	      requests: 2,
	      tokens: 27700,
	      cost: 0.06,
	      standard_cost: 0.06,
	      user_cost: 0.06
	    }
	  },
	  seven_day: {
	    utilization: 0,
	    resets_at: null,
	    remaining_seconds: 0,
	    window_stats: {
	      requests: 2,
	      tokens: 27700,
	      cost: 0.06,
	      standard_cost: 0.06,
	      user_cost: 0.06
	    }
	  }
	})

		const wrapper = mount(AccountUsageCell, {
		  props: {
		    account: makeAccount({
		      id: 2002,
		      platform: 'openai',
		      type: 'oauth',
		      extra: {}
		    })
		  },
	  global: {
	    stubs: {
	      UsageProgressBar: {
	        props: ['label', 'utilization', 'resetsAt', 'windowStats', 'color'],
	        template: '<div class="usage-bar">{{ label }}|{{ utilization }}|{{ windowStats?.tokens }}</div>'
	      },
	      AccountQuotaInfo: true
	    }
	  }
	})

	await flushPromises()

	expect(getUsage).toHaveBeenCalledWith(2002)
	expect(wrapper.text()).toContain('5h|0|27700')
	expect(wrapper.text()).toContain('7d|0|27700')
  })

  it('OpenAI OAuth 在行数据刷新但仍无 codex 快照时会重新拉取 usage', async () => {
	getUsage
	  .mockResolvedValueOnce({
	    five_hour: {
	      utilization: 0,
	      resets_at: null,
	      remaining_seconds: 0,
	      window_stats: {
	        requests: 1,
	        tokens: 100,
	        cost: 0.01,
	        standard_cost: 0.01,
	        user_cost: 0.01
	      }
	    },
	    seven_day: null
	  })
	  .mockResolvedValueOnce({
	    five_hour: {
	      utilization: 0,
	      resets_at: null,
	      remaining_seconds: 0,
	      window_stats: {
	        requests: 2,
	        tokens: 200,
	        cost: 0.02,
	        standard_cost: 0.02,
	        user_cost: 0.02
	      }
	    },
	    seven_day: null
	  })

		const wrapper = mount(AccountUsageCell, {
		  props: {
		    account: makeAccount({
		      id: 2003,
		      platform: 'openai',
		      type: 'oauth',
		      updated_at: '2026-03-07T10:00:00Z',
		      extra: {}
		    })
		  },
	  global: {
	    stubs: {
	      UsageProgressBar: {
	        props: ['label', 'utilization', 'resetsAt', 'windowStats', 'color'],
	        template: '<div class="usage-bar">{{ label }}|{{ utilization }}|{{ windowStats?.tokens }}</div>'
	      },
	      AccountQuotaInfo: true
	    }
	  }
	})

	await flushPromises()
	expect(wrapper.text()).toContain('5h|0|100')
	expect(getUsage).toHaveBeenCalledTimes(1)

	await wrapper.setProps({
	  account: {
	    id: 2003,
	    platform: 'openai',
	    type: 'oauth',
	    updated_at: '2026-03-07T10:01:00Z',
	    extra: {}
	  }
	})

	await flushPromises()
	expect(getUsage).toHaveBeenCalledTimes(2)
	expect(wrapper.text()).toContain('5h|0|200')
  })

  it('OpenAI OAuth 已限额时显示 /usage API 返回的限额数据', async () => {
	getUsage.mockResolvedValue({
	  five_hour: {
	    utilization: 100,
	    resets_at: '2026-03-07T12:00:00Z',
	    remaining_seconds: 3600,
	    window_stats: {
	      requests: 211,
	      tokens: 106540000,
	      cost: 38.13,
	      standard_cost: 38.13,
	      user_cost: 38.13
	    }
	  },
	  seven_day: {
	    utilization: 100,
	    resets_at: '2026-03-13T12:00:00Z',
	    remaining_seconds: 3600,
	    window_stats: {
	      requests: 211,
	      tokens: 106540000,
	      cost: 38.13,
	      standard_cost: 38.13,
	      user_cost: 38.13
	    }
	  }
	})

		const wrapper = mount(AccountUsageCell, {
		  props: {
		    account: makeAccount({
		      id: 2004,
		      platform: 'openai',
		      type: 'oauth',
		      rate_limit_reset_at: '2099-03-07T12:00:00Z',
		      extra: {
		        codex_5h_used_percent: 0,
		        codex_7d_used_percent: 0
		      }
		    })
		  },
	  global: {
	    stubs: {
	      UsageProgressBar: {
	        props: ['label', 'utilization', 'resetsAt', 'windowStats', 'color'],
	        template: '<div class="usage-bar">{{ label }}|{{ utilization }}|{{ windowStats?.tokens }}</div>'
	      },
	      AccountQuotaInfo: true
	    }
	  }
	})

	await flushPromises()

  expect(getUsage).toHaveBeenCalledWith(2004)
  expect(wrapper.text()).toContain('5h|100|106540000')
  expect(wrapper.text()).toContain('7d|100|106540000')
  })

  it('Key 账号会展示 today stats 徽章并带 A/U 提示', async () => {
		const wrapper = mount(AccountUsageCell, {
		  props: {
		    account: makeAccount({
		      id: 3001,
		      platform: 'anthropic',
		      type: 'apikey'
		    }),
		    todayStats: {
		      requests: 1_000_000,
		      tokens: 1_000_000_000,
		      cost: 12.345,
		      standard_cost: 12.345,
		      user_cost: 6.789,
		      balance_cost: 1.789,
		      subscription_cost: 5,
		      subscription_revenue: 3.2
		    }
		  },
		  global: {
		    stubs: {
		      UsageProgressBar: true,
		      AccountQuotaInfo: true
		    }
		  }
		})

		await flushPromises()

		expect(wrapper.text()).toContain('1.0M req')
		expect(wrapper.text()).toContain('1.0B')
		expect(wrapper.text()).toContain('A $12.35')
		// U = balance 1.789 + subscription $5 × 0.64, without a currency.
		expect(wrapper.get('[data-testid="today-revenue"]').text()).toBe('U 4.99')

		const badges = wrapper.findAll('span[title]')
		expect(badges.some(node => node.attributes('title') === 'usage.accountBilled')).toBe(true)
		expect(badges.some(node => node.attributes('title')?.startsWith('usage.revenueHint'))).toBe(true)
  })

  it('Key 账号在 today stats loading 时显示骨架屏', async () => {
		const wrapper = mount(AccountUsageCell, {
		  props: {
		    account: makeAccount({
		      id: 3002,
		      platform: 'anthropic',
		      type: 'apikey'
		    }),
		    todayStats: null,
		    todayStatsLoading: true
		  },
		  global: {
		    stubs: {
		      UsageProgressBar: true,
		      AccountQuotaInfo: true
		    }
		  }
		})

		await flushPromises()

		expect(wrapper.findAll('.animate-pulse').length).toBeGreaterThan(0)
  })

  it('Key 账号在无 today stats 且无配额时显示兜底短横线', async () => {
		const wrapper = mount(AccountUsageCell, {
		  props: {
		    account: makeAccount({
		      id: 3003,
		      platform: 'anthropic',
		      type: 'apikey',
		      quota_limit: 0,
		      quota_daily_limit: 0,
		      quota_weekly_limit: 0
		    }),
		    todayStats: null,
		    todayStatsLoading: false
		  },
		  global: {
		    stubs: {
		      UsageProgressBar: true,
		      AccountQuotaInfo: true
		    }
		  }
		})

		await flushPromises()

		expect(wrapper.text().trim()).toBe('-')
  })
})
