import type {
  DepthData,
  ExecReport,
  KlineData,
  OrderRow,
  TradeData,
} from '../api/envelope'
import { createStore } from './createStore'

export const MAX_TRADES = 60
export const MAX_KLINES = 500
export const MAX_ORDERS = 200

export interface MetaState {
  symbol: string
  pricePrecision: number
  quantityPrecision: number
  ready: boolean
  topics: string[]
}

export type ConnectionStatus = 'idle' | 'connecting' | 'open' | 'closed'

export interface ConnectionState {
  status: ConnectionStatus
  attempts: number
  latencyMs: number | null
  gaps: number
  lastSeq: number
}

export const metaStore = createStore<MetaState>({
  symbol: 'BTCUSDT',
  pricePrecision: 8,
  quantityPrecision: 8,
  ready: false,
  topics: [],
})

export const connectionStore = createStore<ConnectionState>({
  status: 'idle',
  attempts: 0,
  latencyMs: null,
  gaps: 0,
  lastSeq: 0,
})

export const depthStore = createStore<DepthData | null>(null)
export const tradesStore = createStore<TradeData[]>([])
export const klinesStore = createStore<KlineData[]>([])
export const ordersStore = createStore<OrderRow[]>([])

export function setDepth(depth: DepthData): void {
  depthStore.set(depth)
}

/** Replace the tape with a server snapshot. */
export function replaceTrades(trades: TradeData[]): void {
  tradesStore.set(trades.slice(0, MAX_TRADES))
}

/** Prepend live prints, de-duplicating by venue trade id. */
export function prependTrades(incoming: TradeData[]): void {
  if (incoming.length === 0) return

  tradesStore.set((prev) => {
    const seen = new Set<number>()
    const out: TradeData[] = []

    // Incoming is chronological, so walk it backwards to keep newest first.
    for (let i = incoming.length - 1; i >= 0; i -= 1) {
      const t = incoming[i]
      if (seen.has(t.id)) continue
      seen.add(t.id)
      out.push(t)
    }
    for (const t of prev) {
      if (seen.has(t.id)) continue
      seen.add(t.id)
      out.push(t)
    }
    return out.slice(0, MAX_TRADES)
  })
}

/** Replace candles with a snapshot. */
export function replaceKlines(klines: KlineData[]): void {
  const sorted = [...klines].sort((a, b) => a.open_time - b.open_time)
  klinesStore.set(sorted.slice(-MAX_KLINES))
}

/** Merge live candle updates; an update for an existing bucket replaces it. */
export function mergeKlines(incoming: KlineData[]): void {
  if (incoming.length === 0) return

  klinesStore.set((prev) => {
    const byTime = new Map<number, KlineData>()
    for (const k of prev) byTime.set(k.open_time, k)
    for (const k of incoming) byTime.set(k.open_time, k)
    return [...byTime.values()]
      .sort((a, b) => a.open_time - b.open_time)
      .slice(-MAX_KLINES)
  })
}

/** Merge a REST page of orders, preferring the newer copy per order id. */
export function mergeOrders(rows: OrderRow[]): void {
  ordersStore.set((prev) => {
    const byId = new Map<string, OrderRow>()
    for (const row of prev) byId.set(row.order_id, row)
    for (const row of rows) {
      const existing = byId.get(row.order_id)
      byId.set(
        row.order_id,
        existing && existing.updated_at > row.updated_at ? existing : row,
      )
    }
    return [...byId.values()]
      .sort((a, b) => b.created_at - a.created_at)
      .slice(0, MAX_ORDERS)
  })
}

/** Apply one execution report pushed over the WebSocket. */
export function upsertExecReport(report: ExecReport): void {
  ordersStore.set((prev) => {
    const index = prev.findIndex((o) => o.order_id === report.order_id)

    if (index >= 0) {
      const next = [...prev]
      next[index] = {
        ...next[index],
        status: report.status,
        filled: report.filled,
        leaves: report.leaves,
        reason: report.reason ?? '',
        updated_at: report.timestamp,
      }
      return next
    }

    // The report arrived before the REST page did; keep a minimal placeholder
    // so the row is visible immediately.
    const placeholder: OrderRow = {
      order_id: report.order_id,
      account_id: 0,
      symbol: '',
      side: 'BUY',
      type: '',
      time_in_force: '',
      price: 0,
      quantity: '',
      status: report.status,
      filled: report.filled,
      leaves: report.leaves,
      reason: report.reason ?? '',
      created_at: report.timestamp,
      updated_at: report.timestamp,
    }

    return [placeholder, ...prev]
      .sort((a, b) => b.created_at - a.created_at)
      .slice(0, MAX_ORDERS)
  })
}
