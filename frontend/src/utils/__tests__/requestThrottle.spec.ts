import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import { createRequestThrottle, runThrottledWithRetry } from '../requestThrottle'

describe('createRequestThrottle', () => {
  beforeEach(() => {
    vi.useFakeTimers()
    vi.setSystemTime(0)
  })
  afterEach(() => {
    vi.useRealTimers()
  })

  it('starts up to capacity immediately, then paces the rest in order', async () => {
    const throttle = createRequestThrottle({ capacity: 3, refillPerSecond: 10 })
    const started: number[] = []
    const all = Array.from({ length: 6 }, (_, i) =>
      throttle.run(async () => {
        started.push(i)
        return i
      })
    )

    await vi.advanceTimersByTimeAsync(0)
    expect(started).toEqual([0, 1, 2])
    await vi.advanceTimersByTimeAsync(100)
    expect(started).toEqual([0, 1, 2, 3])
    await vi.advanceTimersByTimeAsync(200)
    expect(started).toEqual([0, 1, 2, 3, 4, 5])
    await expect(Promise.all(all)).resolves.toEqual([0, 1, 2, 3, 4, 5])
  })

  it('keeps a 100-request burst within the edge limit (burst 80 + 20 r/s)', async () => {
    const throttle = createRequestThrottle({ capacity: 40, refillPerSecond: 10 })
    const startTimes: number[] = []
    const all = Array.from({ length: 100 }, () =>
      throttle.run(async () => {
        startTimes.push(Date.now())
      })
    )
    await vi.advanceTimersByTimeAsync(10_000)
    await Promise.all(all)
    expect(startTimes).toHaveLength(100)

    // Simulate nginx limit_req (rate 20 r/s, burst 80, nodelay): never rejects.
    let excess = 0
    let last = 0
    for (const t of startTimes) {
      excess = Math.max(0, excess - ((t - last) * 20) / 1000) + 1
      last = t
      expect(excess).toBeLessThanOrEqual(81)
    }
  })

  it('lets priority requests jump ahead of waiting ones', async () => {
    const throttle = createRequestThrottle({ capacity: 1, refillPerSecond: 10 })
    const started: string[] = []
    const run = (name: string, priority = false) =>
      throttle.run(async () => {
        started.push(name)
      }, { priority })
    const all = [run('a'), run('b'), run('c'), run('manual', true)]
    await vi.advanceTimersByTimeAsync(1000)
    await Promise.all(all)
    expect(started).toEqual(['a', 'manual', 'b', 'c'])
  })

  it('turns a synchronous throw into a rejection and keeps draining', async () => {
    const throttle = createRequestThrottle({ capacity: 1, refillPerSecond: 10 })
    const failing = throttle.run((() => {
      throw new Error('sync')
    }) as () => Promise<never>)
    const next = throttle.run(async () => 'ok')
    await expect(failing).rejects.toThrow('sync')
    await vi.advanceTimersByTimeAsync(100)
    await expect(next).resolves.toBe('ok')
  })

  it('propagates task errors without blocking the queue', async () => {
    const throttle = createRequestThrottle({ capacity: 1, refillPerSecond: 10 })
    const failing = throttle.run(async () => {
      throw new Error('boom')
    })
    const next = throttle.run(async () => 'ok')
    await expect(failing).rejects.toThrow('boom')
    await vi.advanceTimersByTimeAsync(100)
    await expect(next).resolves.toBe('ok')
  })
})

describe('runThrottledWithRetry', () => {
  beforeEach(() => {
    vi.useFakeTimers()
  })
  afterEach(() => {
    vi.useRealTimers()
  })

  it('retries edge rate-limit rejections (503) and then succeeds', async () => {
    const throttle = createRequestThrottle({ capacity: 10, refillPerSecond: 10 })
    const task = vi
      .fn<() => Promise<string>>()
      .mockRejectedValueOnce({ status: 503 })
      .mockResolvedValueOnce('ok')
    const result = runThrottledWithRetry(throttle, task)
    await vi.advanceTimersByTimeAsync(2000)
    await expect(result).resolves.toBe('ok')
    expect(task).toHaveBeenCalledTimes(2)
  })

  it('does not retry other errors', async () => {
    const throttle = createRequestThrottle({ capacity: 10, refillPerSecond: 10 })
    const task = vi.fn<() => Promise<string>>().mockRejectedValue({ status: 500 })
    await expect(runThrottledWithRetry(throttle, task)).rejects.toEqual({ status: 500 })
    expect(task).toHaveBeenCalledTimes(1)
  })

  it('gives up after the retry budget', async () => {
    const throttle = createRequestThrottle({ capacity: 10, refillPerSecond: 10 })
    const task = vi.fn<() => Promise<string>>().mockRejectedValue({ status: 503 })
    const result = runThrottledWithRetry(throttle, task, 2, 100)
    const assertion = expect(result).rejects.toEqual({ status: 503 })
    await vi.advanceTimersByTimeAsync(1000)
    await assertion
    expect(task).toHaveBeenCalledTimes(3)
  })
})
