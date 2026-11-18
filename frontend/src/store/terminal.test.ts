import { beforeEach, describe, expect, it } from 'vitest'

import type { DepthData, KlineData, TradeData } from '../api/envelope'
import {
  MAX_TRADES,
  depthStore,
  klinesStore,
  mergeKlines,
  prependTrades,
  replaceKlines,
  setDepth,
  tradesStore,
} from './terminal'

function depth(
  updateId: number,
  bids: DepthData['bids'],
  asks: DepthData['asks'] = [],
): DepthData {
  return {
    symbol: 'BTCUSDT',
    last_update_id: updateId,
    bids,
    asks,
    ts: updateId,
  }
}

function trade(id: number): TradeData {
  return {
    id,
    symbol: 'BTCUSDT',
    price: '100.00',
    qty: '1.00',
    side: 'BUY',
    ts: id,
  }
}

function candle(openTime: number, close: string): KlineData {
  return {
    symbol: 'BTCUSDT',
    interval: '1m',
    open_time: openTime,
    open: '100.00',
    high: '110.00',
    low: '90.00',
    close,
    volume: '12.00',
    closed: false,
    ts: openTime,
  }
}

beforeEach(() => {
  depthStore.set(null)
  tradesStore.set([])
  klinesStore.set([])
})

describe('setDepth', () => {
  it('replaces the whole book instead of merging levels', () => {
    const first = depth(
      1,
      [
        ['100.00', '2.00'],
        ['99.00', '3.00'],
      ],
      [['101.00', '1.00']],
    )
    setDepth(first)

    const second = depth(2, [['102.00', '0.50']], [])
    setDepth(second)

    expect(depthStore.get()).toBe(second)
    expect(depthStore.get()?.bids).toEqual([['102.00', '0.50']])
    expect(depthStore.get()?.asks).toEqual([])
  })
})

describe('mergeKlines', () => {
  it('overwrites the candle with the same open_time', () => {
    replaceKlines([candle(1000, '100.00'), candle(2000, '101.00')])

    mergeKlines([candle(2000, '105.00')])

    const rows = klinesStore.get()
    expect(rows).toHaveLength(2)
    expect(rows.map((k) => k.open_time)).toEqual([1000, 2000])
    expect(rows[1].close).toBe('105.00')
  })

  it('inserts a new bucket and keeps the series sorted', () => {
    replaceKlines([candle(1000, '100.00'), candle(3000, '103.00')])

    mergeKlines([candle(2000, '102.00')])

    expect(klinesStore.get().map((k) => k.open_time)).toEqual([1000, 2000, 3000])
  })
})

describe('prependTrades', () => {
  it('prepends newest first and de-duplicates by trade id', () => {
    tradesStore.set([trade(1), trade(2), trade(3)])

    prependTrades([trade(2), trade(4), trade(5)])

    const ids = tradesStore.get().map((t) => t.id)
    expect(ids).toEqual([5, 4, 2, 1, 3])
    expect(new Set(ids).size).toBe(ids.length)
  })

  it('keeps only the newest MAX_TRADES prints', () => {
    prependTrades(
      Array.from({ length: MAX_TRADES + 10 }, (_, index) => trade(index + 1)),
    )

    const ids = tradesStore.get().map((t) => t.id)
    expect(MAX_TRADES).toBe(60)
    expect(ids).toHaveLength(MAX_TRADES)
    expect(ids[0]).toBe(70)
    expect(ids[ids.length - 1]).toBe(11)
    expect(ids).not.toContain(1)
  })
})
