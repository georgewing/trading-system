// Pure helpers for risk events: wire decoding, de-duplication and display
// class selection. Kept free of React and store imports so they stay testable.

import type { RiskAction, RiskEvent } from '../api/envelope'

const ACTION_CLASS: Record<RiskAction, string> = {
  ALLOW: 'risk-allow',
  CANCEL_ONLY: 'risk-warn',
  PAUSE: 'risk-warn',
  REJECT: 'risk-block',
  HALT: 'risk-block',
}

/**
 * Identity of one risk decision.
 *
 * REST rows carry an id. WebSocket events do not, and one decision can be
 * replayed after a reconnect, so the same tuple of fields is treated as the
 * same event. `message` is included to keep distinct rejections of one order
 * apart.
 */
export function riskEventKey(event: RiskEvent): string {
  if (typeof event.id === 'number') return `id:${event.id}`

  return [
    event.ts,
    event.account_id,
    event.symbol,
    event.code,
    event.action,
    event.message,
  ].join('|')
}

/** Badge class for an action. Unknown actions fall back to the blocking style. */
export function riskActionClass(action: string): string {
  return ACTION_CLASS[action as RiskAction] ?? 'risk-block'
}

/** Narrow untyped wire data to a risk event without trusting the payload. */
export function isRiskEvent(value: unknown): value is RiskEvent {
  if (typeof value !== 'object' || value === null) return false
  const candidate = value as Record<string, unknown>
  return (
    typeof candidate.ts === 'number' &&
    typeof candidate.code === 'string' &&
    typeof candidate.action === 'string'
  )
}

/** `risk.snapshot` carries a bare array; tolerate a `{events: []}` wrapper. */
export function parseRiskSnapshot(data: unknown): RiskEvent[] {
  if (Array.isArray(data)) return data.filter(isRiskEvent)

  const wrapped = data as { events?: unknown } | null
  if (wrapped && Array.isArray(wrapped.events)) {
    return wrapped.events.filter(isRiskEvent)
  }
  return []
}

/** Single-event payloads (`risk.blocked`, `risk.event`). */
export function parseRiskEvent(data: unknown): RiskEvent | null {
  return isRiskEvent(data) ? data : null
}
