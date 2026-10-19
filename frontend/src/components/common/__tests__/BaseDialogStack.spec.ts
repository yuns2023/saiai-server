import { describe, it, expect } from 'vitest'
import { mount } from '@vue/test-utils'
import { nextTick } from 'vue'
import BaseDialog from '../BaseDialog.vue'

describe('stacked request and error dialogs', () => {
  it('closes only the error on Escape and retains the scroll lock until the list closes', async () => {
    const list = mount(BaseDialog, { props: { show: true, title: 'Requests', closeOnEscape: false, width: 'viewport', zIndex: 40 } })
    const detail = mount(BaseDialog, { props: { show: true, title: 'Error' } })
    await nextTick()
    document.dispatchEvent(new KeyboardEvent('keydown', { key: 'Escape' }))
    expect(detail.emitted('close')).toHaveLength(1)
    expect(list.emitted('close')).toBeUndefined()
    await detail.setProps({ show: false })
    detail.unmount()
    expect(document.body.classList.contains('modal-open')).toBe(true)
    await list.setProps({ closeOnEscape: true })
    document.dispatchEvent(new KeyboardEvent('keydown', { key: 'Escape' }))
    expect(list.emitted('close')).toHaveLength(1)
    list.unmount()
    expect(document.body.classList.contains('modal-open')).toBe(false)
  })
})
