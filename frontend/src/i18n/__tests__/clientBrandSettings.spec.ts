import { readFileSync } from 'node:fs'
import { resolve } from 'node:path'
import { describe, expect, it } from 'vitest'
import en from '../locales/en'
import zh from '../locales/zh'

const keys = [
  'clientBrandName',
  'clientBrandNamePlaceholder',
  'clientBrandNameHint',
  'clientSetupCommand',
  'clientSetupCommandPlaceholder',
  'clientSetupCommandHint'
] as const

describe('client brand settings', () => {
  it('has English and Chinese texts that explain the empty default', () => {
    for (const key of keys) {
      expect(en.admin.settings.site[key]).toBeTruthy()
      expect(zh.admin.settings.site[key]).toBeTruthy()
    }
    expect(zh.admin.settings.site.clientBrandNameHint).toContain('留空 = 保持原 SAIAI 文案')
    expect(zh.admin.settings.site.clientSetupCommandHint).toContain('留空 = 保持原 SAIAI 文案')
    expect(en.admin.settings.site.clientBrandNameHint).toContain('keep the original SAIAI wording')
  })

  it('edits and saves both fields on the admin settings page', () => {
    const view = readFileSync(resolve(process.cwd(), 'src/views/admin/SettingsView.vue'), 'utf8')
    expect(view).toContain('v-model="form.client_brand_name"')
    expect(view).toContain('v-model="form.client_setup_command"')
    expect(view).toContain('client_brand_name: form.client_brand_name')
    expect(view).toContain('client_setup_command: form.client_setup_command')
  })
})
