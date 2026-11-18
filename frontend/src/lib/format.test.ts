import { describe, expect, it } from 'vitest'

import {
  formatPrice,
  scaledToDecimal,
  ticksToPrice,
  toTicks,
} from './format'

describe('toTicks', () => {
  it('converts decimal prices to integer ticks', () => {
    expect(toTicks('123.45', 2)).toBe(12345)
    expect(toTicks('0.00000001', 8)).toBe(1)
    expect(toTicks('1000', 0)).toBe(1000)
  })

  it('rejects non-positive values', () => {
    expect(toTicks('0', 2)).toBeNull()
    expect(toTicks('0.00', 2)).toBeNull()
    expect(toTicks('-1.23', 2)).toBeNull()
  })

  it('rejects values that are not finite numbers', () => {
    expect(toTicks('abc', 2)).toBeNull()
    expect(toTicks('Infinity', 2)).toBeNull()
    expect(toTicks('', 2)).toBeNull()
  })

  it('rejects ticks beyond Number.MAX_SAFE_INTEGER', () => {
    expect(toTicks('90071992.54740992', 8)).toBeNull()
  })
})

describe('ticksToPrice / scaledToDecimal', () => {
  it('formats ticks with the requested precision', () => {
    expect(ticksToPrice(12345, 2)).toBe('123.45')
    expect(ticksToPrice(1, 8)).toBe('0.00000001')
    expect(ticksToPrice(1000, 0)).toBe('1000')
    expect(ticksToPrice(-12345, 2)).toBe('-123.45')
  })

  it('keeps trailing zeros for scaled decimals', () => {
    expect(scaledToDecimal(0, 2)).toBe('0.00')
    expect(scaledToDecimal(5, 2)).toBe('0.05')
  })

  it('returns zero for non-finite input', () => {
    expect(scaledToDecimal(Number.NaN, 2)).toBe('0')
    expect(scaledToDecimal(Number.POSITIVE_INFINITY, 2)).toBe('0')
  })
})

describe('formatPrice', () => {
  it('keeps at least two decimals', () => {
    expect(formatPrice('123')).toBe('123.00')
    expect(formatPrice('123.4')).toBe('123.40')
    expect(formatPrice('0.5')).toBe('0.50')
  })

  it('trims trailing zeros beyond two decimals', () => {
    expect(formatPrice('123.45000000')).toBe('123.45')
    expect(formatPrice('1.2300')).toBe('1.23')
  })

  it('groups thousands', () => {
    expect(formatPrice('1234.5')).toBe('1,234.50')
    expect(formatPrice('1000000')).toBe('1,000,000.00')
  })

  it('rounds to maxDp', () => {
    expect(formatPrice('123.45678901', 2, 2)).toBe('123.46')
  })

  it('passes non-numeric input through', () => {
    expect(formatPrice('n/a')).toBe('n/a')
  })
})
