import { beforeEach, describe, expect, it, vi } from 'vitest'
import { flushPromises, mount } from '@vue/test-utils'
import KeyAdvancedDialog from '../KeyAdvancedDialog.vue'

const { copyToClipboard } = vi.hoisted(() => ({ copyToClipboard: vi.fn() }))
vi.mock('vue-i18n', () => ({ useI18n: () => ({ t: (key: string) => key }) }))
vi.mock('@/composables/useClipboard', () => ({
  useClipboard: () => ({ copyToClipboard })
}))

function mountDialog() {
  return mount(KeyAdvancedDialog, {
    props: { show: true, apiKey: 'TEST_ONLY_RAW_KEY', keyName: 'Example' },
    global: {
      stubs: {
        BaseDialog: {
          props: ['show'],
          template: '<div v-if="show"><slot /><slot name="footer" /></div>'
        },
        Icon: true
      }
    }
  })
}

describe('KeyAdvancedDialog', () => {
  beforeEach(() => { copyToClipboard.mockReset().mockResolvedValue(true) })

  it('hides the raw key initially and does not copy automatically', () => {
    const wrapper = mountDialog()
    expect(wrapper.text()).not.toContain('TEST_ONLY_RAW_KEY')
    expect(wrapper.find('code').text()).toContain('••••')
    expect(copyToClipboard).not.toHaveBeenCalled()
  })

  it('reveals and hides only after explicit actions', async () => {
    const wrapper = mountDialog()
    await wrapper.find('button[aria-pressed]').trigger('click')
    expect(wrapper.find('code').text()).toBe('TEST_ONLY_RAW_KEY')
    await wrapper.find('button[aria-pressed]').trigger('click')
    expect(wrapper.text()).not.toContain('TEST_ONLY_RAW_KEY')
  })

  it('copies the raw key without revealing it or claiming configuration success', async () => {
    const wrapper = mountDialog()
    await wrapper.findAll('button')[1].trigger('click')
    await flushPromises()
    expect(copyToClipboard).toHaveBeenCalledWith('TEST_ONLY_RAW_KEY', 'keys.copied')
    expect(wrapper.findAll('button')[1].text()).toBe('keys.copied')
    expect(wrapper.text()).not.toContain('TEST_ONLY_RAW_KEY')
  })

  it('resets reveal and copy feedback after closing and reopening', async () => {
    const wrapper = mountDialog()
    await wrapper.find('button[aria-pressed]').trigger('click')
    await wrapper.findAll('button')[1].trigger('click')
    await wrapper.setProps({ show: false })
    expect(wrapper.find('code').exists()).toBe(false)
    await wrapper.setProps({ show: true })
    expect(wrapper.text()).not.toContain('TEST_ONLY_RAW_KEY')
    expect(wrapper.findAll('button')[1].text()).toBe('keys.copyKey')
  })

  it('resets reveal when switching to a different key', async () => {
    const wrapper = mountDialog()
    await wrapper.find('button[aria-pressed]').trigger('click')
    await wrapper.setProps({ apiKey: 'TEST_ONLY_OTHER_KEY' })
    expect(wrapper.text()).not.toContain('TEST_ONLY_OTHER_KEY')
    expect(wrapper.text()).not.toContain('TEST_ONLY_RAW_KEY')
  })

  it('does not display copied feedback after a failed clipboard action', async () => {
    copyToClipboard.mockResolvedValue(false)
    const wrapper = mountDialog()
    await wrapper.findAll('button')[1].trigger('click')
    await flushPromises()
    expect(wrapper.findAll('button')[1].text()).toBe('keys.copyKey')
  })

  it('ignores late copy feedback after the key changes', async () => {
    let finishCopy: (success: boolean) => void = () => {}
    copyToClipboard.mockImplementation(() => new Promise<boolean>((resolve) => { finishCopy = resolve }))
    const wrapper = mountDialog()
    await wrapper.findAll('button')[1].trigger('click')
    await wrapper.setProps({ apiKey: 'TEST_ONLY_OTHER_KEY' })
    finishCopy(true)
    await flushPromises()
    expect(wrapper.findAll('button')[1].text()).toBe('keys.copyKey')
  })
})
