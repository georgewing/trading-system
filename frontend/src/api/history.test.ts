import { describe, expect, it, vi } from 'vitest'

import type { KlineData } from './envelope'
import { fetchKlineHistory } from './history'

function jsonResponse(body: unknown, status = 200): Response {
  return {
    ok: status >= 200 && status < 300,
    status,
    json: async () => body,
  } as unknown as Response
}

const candle: KlineData = {
  symbol: 'BTCUSDT',
  interval: '1m',
  open_time: 1700000000000,
  open: '62518.01000000',
  high: '62528.01000000',
  low: '62508.01000000',
  close: '62520.01000000',
  volume: '123.45000000',
  closed: true,
  ts: 1700000060000,
}

describe('fetchKlineHistory', () => {
  it('parses the kline array from a 200 response', async () => {
    const fetchMock = vi.fn(async (_url: RequestInfo | URL, _init?: RequestInit) =>
      jsonResponse({ symbol: 'BTCUSDT', interval: '1m', klines: [candle] }),
    )
    vi.stubGlobal('fetch', fetchMock)

    const rows = await fetchKlineHistory({ symbol: 'BTCUSDT', limit: 300 })

    expect(rows).toEqual([candle])
    // Decimal fields stay strings; the chart converts them itself.
    expect(typeof rows[0].open).toBe('string')
    expect(fetchMock).toHaveBeenCalledTimes(1)
  })

  it('returns an empty array when the range has no candles', async () => {
    vi.stubGlobal(
      'fetch',
      vi.fn(async () =>
        jsonResponse({ symbol: 'BTCUSDT', interval: '1m', klines: [] }),
      ),
    )

    await expect(
      fetchKlineHistory({ symbol: 'BTCUSDT' }),
    ).resolves.toEqual([])
  })

  it('surfaces the backend message on a 503 response', async () => {
    const message = 'kline history unavailable: clickhouse is not enabled'
    vi.stubGlobal(
      'fetch',
      vi.fn(async () => jsonResponse({ code: 503, message }, 503)),
    )

    await expect(fetchKlineHistory({ symbol: 'BTCUSDT' })).rejects.toThrow(
      message,
    )
  })

  it('sends symbol, from, to and limit and omits absent bounds', async () => {
    const fetchMock = vi.fn(
      async (_url: RequestInfo | URL, _init?: RequestInit) =>
        jsonResponse({ symbol: 'BTCUSDT', interval: '1m', klines: [] }),
    )
    vi.stubGlobal('fetch', fetchMock)

    await fetchKlineHistory({
      symbol: 'BTCUSDT',
      fromMs: 1700000000000,
      toMs: 1700000300000,
      limit: 120,
    })

    const bounded = new URL(String(fetchMock.mock.calls[0][0]), 'http://localhost')
    expect(bounded.pathname).toBe('/api/v1/market/kline/history')
    expect(bounded.searchParams.get('symbol')).toBe('BTCUSDT')
    expect(bounded.searchParams.get('interval')).toBe('1m')
    expect(bounded.searchParams.get('from')).toBe('1700000000000')
    expect(bounded.searchParams.get('to')).toBe('1700000300000')
    expect(bounded.searchParams.get('limit')).toBe('120')

    // ALL sends no lower bound at all instead of from=0.
    await fetchKlineHistory({ symbol: 'BTCUSDT' })

    const open = new URL(String(fetchMock.mock.calls[1][0]), 'http://localhost')
    expect(open.searchParams.has('from')).toBe(false)
    expect(open.searchParams.has('to')).toBe(false)
    expect(open.searchParams.has('limit')).toBe(false)
  })
})
