import { beforeEach, describe, expect, it } from 'vitest'
import type { RiskEvent } from '../api/envelope'
import {
  MAX_RISK_EVENTS,
  appendRiskEvents,
  riskEventStore,
  riskTickStore,
  setRiskSnapshot,
} from './terminal'

function riskEvent(overrides: Partial<RiskEvent> = {}): RiskEvent {
  return {
    ts: 1_700_000_000_000,
    account_id: 1,
    symbol: 'BTCUSDT',
    strategy: 'market_maker',
    order_id: 123,
    code: 'MAX_QTY',
    action: 'REJECT',
    message: 'order exceeds max quantity',
    ...overrides,
  }
}

function events(): RiskEvent[] {
  return riskEventStore.get().events
}

beforeEach(() => {
  setRiskSnapshot([])
})

describe('setRiskSnapshot', () => {
  it('replaces the whole list instead of merging', () => {
    setRiskSnapshot([riskEvent({ ts: 2 }), riskEvent({ ts: 1 })])
    expect(events()).toHaveLength(2)

    const replacement = riskEvent({ ts: 3, message: 'halt' })
    setRiskSnapshot([replacement])

    expect(events()).toEqual([replacement])
  })

  it('stores events newest first even when the server sends them ascending', () => {
    const older = riskEvent({ ts: 1_700_000_000_000, message: 'older' })
    const newer = riskEvent({ ts: 1_700_000_005_000, message: 'newer' })

    setRiskSnapshot([older, newer])
    expect(events()).toEqual([newer, older])
  })

  it('de-duplicates identical decisions inside a snapshot', () => {
    const duplicate = riskEvent()
    setRiskSnapshot([duplicate, { ...duplicate }])
    expect(events()).toHaveLength(1)
  })
})

describe('appendRiskEvents', () => {
  it('prepends new events newest first', () => {
    const first = riskEvent({ ts: 100, message: 'first' })
    const second = riskEvent({ ts: 200, message: 'second' })

    appendRiskEvents([first])
    appendRiskEvents([second])

    expect(events()).toEqual([second, first])
  })

  it('ignores an empty batch without touching the tick counter', () => {
    appendRiskEvents([riskEvent()])
    const tick = riskTickStore.get()

    appendRiskEvents([])

    expect(events()).toHaveLength(1)
    expect(riskTickStore.get()).toBe(tick)
  })

  it('de-duplicates by the decision tuple for id-less WebSocket events', () => {
    const event = riskEvent()

    appendRiskEvents([event])
    appendRiskEvents([{ ...event }])

    expect(events()).toHaveLength(1)
  })

  it('de-duplicates by id when REST rows are replayed', () => {
    appendRiskEvents([riskEvent({ id: 1, ts: 100 })])
    appendRiskEvents([riskEvent({ id: 1, ts: 100, message: 'replayed' })])

    expect(events()).toHaveLength(1)
    expect(events()[0].message).toBe('replayed')
  })

  it('keeps decisions that differ only by message', () => {
    const base = riskEvent({ ts: 500 })
    appendRiskEvents([base, { ...base, message: 'second reason' }])

    expect(events()).toHaveLength(2)
  })

  it(`caps the history at ${MAX_RISK_EVENTS}, dropping the oldest`, () => {
    const many = Array.from({ length: MAX_RISK_EVENTS + 50 }, (_, i) =>
      riskEvent({ ts: i, message: `event-${i}` }),
    )

    appendRiskEvents(many)

    const kept = events()
    expect(kept).toHaveLength(MAX_RISK_EVENTS)
    // Newest first: the highest ts survives, the first 50 are evicted.
    expect(kept[0].ts).toBe(MAX_RISK_EVENTS + 49)
    expect(kept[kept.length - 1].ts).toBe(50)
  })

  it('does not change the store or tick when every event is a duplicate', () => {
    const event = riskEvent()
    appendRiskEvents([event])
    const snapshot = events()
    const tick = riskTickStore.get()

    appendRiskEvents([{ ...event }])

    expect(events()).toBe(snapshot)
    expect(riskTickStore.get()).toBe(tick)
  })

  it('bumps the tick counter when the history changes', () => {
    const before = riskTickStore.get()
    appendRiskEvents([riskEvent()])
    expect(riskTickStore.get()).toBe(before + 1)
  })
})
