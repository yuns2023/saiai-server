/**
 * Client-side token bucket for request bursts.
 *
 * The admin accounts page fires one usage request per row on load/refresh.
 * With ~100 accounts this exceeds the edge nginx per-IP limit
 * (20 r/s, burst 80, rejected with 503), so part of the rows show an error.
 * Requests sharing a throttle start immediately while tokens remain and are
 * then paced at `refillPerSecond`. Priority requests run before normal ones;
 * each class is FIFO.
 */
export interface RequestThrottleOptions {
  capacity: number
  refillPerSecond: number
}

export interface RequestRunOptions {
  /** Queue ahead of already-waiting requests (user-initiated single lookups). */
  priority?: boolean
}

export interface RequestThrottle {
  run<T>(task: () => Promise<T>, options?: RequestRunOptions): Promise<T>
}

export function createRequestThrottle(options: RequestThrottleOptions): RequestThrottle {
  const capacity = Math.max(1, options.capacity)
  const refillPerMs = Math.max(options.refillPerSecond, 0.001) / 1000
  let tokens = capacity
  let lastRefill = Date.now()
  const queue: Array<() => void> = []
  // queue[0 .. priorityCount) are priority entries, in arrival order.
  let priorityCount = 0
  let timer: ReturnType<typeof setTimeout> | null = null

  const refill = () => {
    const now = Date.now()
    tokens = Math.min(capacity, tokens + (now - lastRefill) * refillPerMs)
    lastRefill = now
  }

  const drain = () => {
    timer = null
    refill()
    while (queue.length > 0 && tokens >= 1) {
      tokens -= 1
      if (priorityCount > 0) priorityCount -= 1
      queue.shift()!()
    }
    if (queue.length > 0) {
      timer = setTimeout(drain, Math.ceil((1 - tokens) / refillPerMs))
    }
  }

  return {
    run<T>(task: () => Promise<T>, runOptions?: RequestRunOptions): Promise<T> {
      return new Promise<T>((resolve, reject) => {
        // Promise.resolve().then() turns a synchronous throw into a rejection,
        // so a bad task can never abort drain() and stall the queue.
        const start = () => {
          Promise.resolve().then(task).then(resolve, reject)
        }
        if (runOptions?.priority) {
          queue.splice(priorityCount, 0, start)
          priorityCount += 1
        } else {
          queue.push(start)
        }
        if (timer === null) drain()
      })
    }
  }
}

const RETRYABLE_STATUSES = new Set([429, 503])

const sleep = (ms: number) => new Promise<void>((resolve) => setTimeout(resolve, ms))

/**
 * Runs `task` through `throttle`, retrying when the edge rejected it with
 * 429/503 (rate limited). Other errors are returned as-is.
 */
export async function runThrottledWithRetry<T>(
  throttle: RequestThrottle,
  task: () => Promise<T>,
  retries = 2,
  retryDelayMs = 1500,
  runOptions?: RequestRunOptions
): Promise<T> {
  for (let attempt = 0; ; attempt++) {
    try {
      return await throttle.run(task, runOptions)
    } catch (error) {
      const status = (error as { status?: number } | null)?.status
      if (attempt >= retries || status === undefined || !RETRYABLE_STATUSES.has(status)) {
        throw error
      }
      await sleep(retryDelayMs * (attempt + 1))
    }
  }
}
