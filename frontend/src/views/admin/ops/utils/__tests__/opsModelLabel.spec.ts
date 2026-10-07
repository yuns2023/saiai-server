import { describe, expect, it } from 'vitest'
import { getOpsModelLabel } from '../opsFormatters'

const t = (key: string) => key

describe('Ops model label', () => {
  it('keeps the requested model on failures before selecting an account', () => {
    expect(getOpsModelLabel({ model: 'gpt-requested', request_path: '/v1/responses' }, t)).toBe('gpt-requested')
    expect(getOpsModelLabel({ model: 'auto', request_path: '/chatgpt/backend-api/f/conversation' }, t)).toBe('auto')
  })

  it('identifies native control requests without inventing a model', () => {
    expect(getOpsModelLabel({ model: '', request_path: '/chatgpt/backend-api/conversation/init' }, t)).toBe('admin.ops.errorDetail.noModelInitialization')
    expect(getOpsModelLabel({ model: '', request_path: '/chatgpt/backend-api/models' }, t)).toBe('admin.ops.errorDetail.noModelCatalog')
    expect(getOpsModelLabel({ model: '', request_path: '/chatgpt/backend-api/estuary/content' }, t)).toBe('admin.ops.errorDetail.noModelAsset')
  })

  it('does not mislabel missing model data on real inference paths', () => {
    expect(getOpsModelLabel({ model: '', request_path: '/chatgpt/backend-api/f/conversation' }, t)).toBe('—')
    expect(getOpsModelLabel({ model: '', request_path: '/v1/responses' }, t)).toBe('—')
    expect(getOpsModelLabel({ model: '' }, t)).toBe('—')
  })
})
