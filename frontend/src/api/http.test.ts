import { describe, expect, it, vi } from 'vitest'

import type { SubmitOrderRequest } from './envelope'
import { submitOrder } from './http'
import { toTicks } from '../lib/format'

function jsonResponse(body: unknown, status = 200): Response {
  return {
    ok: status >= 200 && status < 300,
    status,
    json: async () => body,
  } as unknown as Response
}

const baseRequest: SubmitOrderRequest = {
  order_id: 'ord-1',
  account_id: 7,
  symbol: 'BTCUSDT',
  side: 'BUY',
  price: 12345,
  quantity: '0.010000',
  type: 'LIMIT',
  time_in_force: 'GTC',
}

describe('submitOrder', () => {
  it('posts integer price ticks and a string quantity', async () => {
    const fetchMock = vi.fn(
      async (_url: RequestInfo | URL, _init?: RequestInit) =>
        jsonResponse({ status: 'ACCEPTED', order_id: 'ord-1' }),
    )
    vi.stubGlobal('fetch', fetchMock)

    // The UI converts a decimal price to ticks before submitting.
    const price = toTicks('123.45', 2)
    expect(price).toBe(12345)

    const req: SubmitOrderRequest = { ...baseRequest, price: price as number }
    const res = await submitOrder(req)

    expect(res).toEqual({ status: 'ACCEPTED', order_id: 'ord-1' })
    expect(fetchMock).toHaveBeenCalledTimes(1)

    const [url, init] = fetchMock.mock.calls[0]
    expect(url).toBe('/api/v1/order')
    expect(init).toMatchObject({
      method: 'POST',
      headers: { 'Content-Type': 'application/json' },
    })

    const body = JSON.parse(String(init?.body)) as SubmitOrderRequest
    expect(body).toEqual(req)
    expect(Number.isInteger(body.price)).toBe(true)
    expect(body.price).toBe(12345)
    expect(typeof body.quantity).toBe('string')
    expect(body.quantity).toBe('0.010000')
  })

  it('surfaces the API message on a non-2xx response', async () => {
    const fetchMock = vi.fn(async () =>
      jsonResponse({ code: 400, message: 'price must be a positive integer' }, 400),
    )
    vi.stubGlobal('fetch', fetchMock)

    await expect(submitOrder(baseRequest)).rejects.toThrow(
      'price must be a positive integer',
    )
  })

  it('falls back to the status code when the error body is not JSON', async () => {
    const fetchMock = vi.fn(async () => {
      return {
        ok: false,
        status: 503,
        json: async () => {
          throw new Error('not json')
        },
      } as unknown as Response
    })
    vi.stubGlobal('fetch', fetchMock)

    await expect(submitOrder(baseRequest)).rejects.toThrow('HTTP 503')
  })
})
