import { describe, expect, it } from 'vitest'
import type { RiskEvent } from '../api/envelope'
import {
  isRiskEvent,
  parseRiskEvent,
  parseRiskSnapshot,
  riskActionClass,
  riskEventKey,
} from './risk'

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

describe('riskEventKey', () => {
  it('prefers the REST id when present', () => {
    const a = riskEvent({ id: 7 })
    const b = riskEvent({ id: 7, message: 'different' })
    expect(riskEventKey(a)).toBe('id:7')
    expect(riskEventKey(a)).toBe(riskEventKey(b))
  })

  it('falls back to the decision tuple for WebSocket events', () => {
    const a = riskEvent()
    const same = riskEvent()
    const otherMessage = riskEvent({ message: 'other' })

    expect(riskEventKey(a)).not.toContain('id:')
    expect(riskEventKey(a)).toBe(riskEventKey(same))
    expect(riskEventKey(a)).not.toBe(riskEventKey(otherMessage))
  })
})

describe('riskActionClass', () => {
  it('maps blocking actions to the block style', () => {
    expect(riskActionClass('REJECT')).toBe('risk-block')
    expect(riskActionClass('HALT')).toBe('risk-block')
  })

  it('maps throttling actions to the warn style', () => {
    expect(riskActionClass('PAUSE')).toBe('risk-warn')
    expect(riskActionClass('CANCEL_ONLY')).toBe('risk-warn')
  })

  it('maps ALLOW to the allow style', () => {
    expect(riskActionClass('ALLOW')).toBe('risk-allow')
  })

  it('falls back to the block style for unknown actions', () => {
    expect(riskActionClass('SOMETHING_NEW')).toBe('risk-block')
  })
})

describe('wire parsing', () => {
  it('isRiskEvent rejects non-events', () => {
    expect(isRiskEvent(null)).toBe(false)
    expect(isRiskEvent('nope')).toBe(false)
    expect(isRiskEvent({ code: 'X' })).toBe(false)
    expect(isRiskEvent(riskEvent())).toBe(true)
  })

  it('parseRiskSnapshot accepts a bare array and a wrapped payload', () => {
    const events = [riskEvent(), riskEvent({ ts: 2 })]
    expect(parseRiskSnapshot(events)).toEqual(events)
    expect(parseRiskSnapshot({ events })).toEqual(events)
    expect(parseRiskSnapshot({})).toEqual([])
    expect(parseRiskSnapshot(null)).toEqual([])
  })

  it('parseRiskSnapshot drops malformed entries', () => {
    const good = riskEvent()
    expect(parseRiskSnapshot([good, { nonsense: true }])).toEqual([good])
  })

  it('parseRiskEvent returns null for malformed payloads', () => {
    const good = riskEvent()
    expect(parseRiskEvent(good)).toEqual(good)
    expect(parseRiskEvent({ ts: 1 })).toBeNull()
  })
})
