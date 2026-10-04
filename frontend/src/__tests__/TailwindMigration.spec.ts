import { readFileSync } from 'node:fs'
import { resolve } from 'node:path'
import { describe, expect, it } from 'vitest'
import { mount } from '@vue/test-utils'
import { createI18n } from 'vue-i18n'
import postcss from 'postcss'
import tailwindcss from '@tailwindcss/postcss'
import Input from '@/components/common/Input.vue'
import TextArea from '@/components/common/TextArea.vue'
import DataTable from '@/components/common/DataTable.vue'
import Icon from '@/components/icons/Icon.vue'

describe('Tailwind v4 migration', () => {
  it('compiles shared utilities and the retained application theme', async () => {
    const entry = resolve('src/style.css')
    const result = await postcss([tailwindcss()]).process(readFileSync(entry, 'utf8'), {
      from: entry
    })

    expect(result.css).toContain('.btn-primary')
    expect(result.css).toContain('.stat-card')
    expect(result.css).toContain('#14b8a6')
    expect(result.css).toContain('#020617')
    expect(result.css).toContain('.dark *')
    expect(result.css).not.toMatch(/@(?:apply|reference|utility|config)\b/)
  })

  it('makes theme and custom utilities available in referenced Vue styles', async () => {
    const result = await postcss([tailwindcss()]).process(
      '@reference "../style.css"; .migration-probe { @apply btn bg-primary-500 dark:bg-dark-950; }',
      { from: resolve('src/components/migration-probe.css') }
    )

    expect(result.css).toContain('.migration-probe')
    expect(result.css).toContain('inline-flex')
    expect(result.css).toContain('background-color')
    expect(result.css).toContain('.dark *')
    expect(result.css).not.toMatch(/@(?:apply|reference)\b/)
  })

  it('preserves the input blur event instead of renaming it as a CSS utility', async () => {
    const wrapper = mount(Input, { props: { modelValue: '' } })
    await wrapper.get('input').trigger('blur')
    expect(wrapper.emitted('blur')).toHaveLength(1)
    expect(wrapper.emitted('blur-sm')).toBeUndefined()
    wrapper.unmount()
  })

  it('preserves the textarea blur event instead of renaming it as a CSS utility', async () => {
    const wrapper = mount(TextArea, { props: { modelValue: '' } })
    await wrapper.get('textarea').trigger('blur')
    expect(wrapper.emitted('blur')).toHaveLength(1)
    expect(wrapper.emitted('blur-sm')).toBeUndefined()
    wrapper.unmount()
  })

  it('keeps empty table icons at their existing size without competing utilities', () => {
    const i18n = createI18n({
      legacy: false,
      locale: 'en',
      messages: { en: { empty: { noData: 'No data' } } }
    })
    const wrapper = mount(DataTable, {
      props: { data: [], columns: [{ key: 'id', label: 'ID' }] },
      global: { plugins: [i18n] }
    })
    const icons = wrapper.findAllComponents(Icon).filter(icon => icon.props('name') === 'inbox')
    expect(icons).toHaveLength(2)
    for (const icon of icons) {
      expect(icon.props('size')).toBe('xl')
      expect(icon.classes()).toContain('h-8')
      expect(icon.classes()).toContain('w-8')
      expect(icon.classes()).not.toContain('h-12')
      expect(icon.classes()).not.toContain('w-12')
    }
    wrapper.unmount()
  })
})
