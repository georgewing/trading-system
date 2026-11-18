// Formatting helpers. Display-only: prices and quantities stay strings on the
// wire, so nothing here feeds back into an order.

/** Trim trailing zeros, keeping at least minDp decimals. */
export function formatPrice(raw: string, minDp = 2, maxDp = 8): string {
  const n = Number(raw)
  if (!Number.isFinite(n)) return raw

  const [intPart, fracPart = ''] = n.toFixed(maxDp).split('.')
  let frac = fracPart.replace(/0+$/, '')
  if (frac.length < minDp) frac = frac.padEnd(minDp, '0')

  return `${group(intPart)}${frac ? `.${frac}` : ''}`
}

/** Quantities read better with fewer decimals than the raw venue string. */
export function formatQty(raw: string, maxDp = 6): string {
  const n = Number(raw)
  if (!Number.isFinite(n)) return raw
  if (n === 0) return '0'

  const [intPart, fracPart = ''] = n.toFixed(maxDp).split('.')
  const frac = fracPart.replace(/0+$/, '')
  return `${group(intPart)}${frac ? `.${frac}` : ''}`
}

/** Convert integer ticks back to a decimal string. */
export function ticksToPrice(ticks: number, precision: number): string {
  return scaledToDecimal(ticks, precision)
}

/** Convert a scaled integer (price ticks or lot-sized quantity) to decimal. */
export function scaledToDecimal(ticks: number, precision: number): string {
  if (!Number.isFinite(ticks)) return '0'
  if (precision <= 0) return String(ticks)

  const negative = ticks < 0
  const digits = String(Math.abs(ticks)).padStart(precision + 1, '0')
  const head = digits.slice(0, digits.length - precision)
  const tail = digits.slice(digits.length - precision)

  return `${negative ? '-' : ''}${head}.${tail}`
}

/**
 * Convert a decimal price string to integer ticks.
 * Returns null when the value is not a positive, exactly representable number.
 */
export function toTicks(price: string, precision: number): number | null {
  const n = Number(price)
  if (!Number.isFinite(n) || n <= 0) return null

  const ticks = Math.round(n * 10 ** precision)
  if (!Number.isSafeInteger(ticks) || ticks <= 0) return null
  return ticks
}

/** HH:MM:SS for a unix-millisecond timestamp. */
export function formatClock(ms: number): string {
  const d = new Date(ms)
  return [d.getHours(), d.getMinutes(), d.getSeconds()]
    .map((n) => String(n).padStart(2, '0'))
    .join(':')
}

function group(intPart: string): string {
  return intPart.replace(/\B(?=(\d{3})+(?!\d))/g, ',')
}
