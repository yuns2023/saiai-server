import { describe, expect, it } from 'vitest'
import type { AxiosAdapter } from 'axios'
import { api } from '../client'

const body = { code: 400, reason: 'SELF_ONBOARD_PROXY_INVALID', message: 'proxy invalid' }

function adapter(status: number): AxiosAdapter {
  return async (config) => {
    const res = { data: body, status, statusText: '', headers: {}, config }
    if (status >= 400) {
      const { AxiosError } = await import('axios')
      throw new AxiosError('failed', 'ERR_BAD_REQUEST', config, null, res)
    }
    return res
  }
}

describe('api client error reason', () => {
  it('keeps the structured reason for HTTP errors and for code≠0 envelopes alike', async () => {
    for (const status of [400, 200]) {
      await expect(api.post('/x', {}, { adapter: adapter(status) })).rejects.toMatchObject({ status, reason: 'SELF_ONBOARD_PROXY_INVALID', message: 'proxy invalid' })
    }
  })
})
