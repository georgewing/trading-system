// Wire contract shared with the Go backend. Keep this file in sync with
// internal/transport/ws/server.go and internal/transport/http/market.go.

export type OrderStatus =
  | 'NEW'
  | 'PARTIALLY_FILLED'
  | 'FILLED'
  | 'CANCELED'
  | 'REJECTED'
  | 'UNKNOWN'

export type TradeSide = 'BUY' | 'SELL'

export interface Envelope<T = unknown> {
  v: number
  seq: number
  ts: number
  type: string
  symbol?: string
  data: T
}

export interface HelloPayload {
  server_time: number
  symbol: string
  price_precision: number
  quantity_precision: number
  topics: string[]
}

/** [price, quantity] as decimal strings, never floats. */
export type PriceLevel = [string, string]

export interface DepthData {
  symbol: string
  last_update_id: number
  bids: PriceLevel[]
  asks: PriceLevel[]
  ts: number
}

export interface TradeData {
  id: number
  symbol: string
  price: string
  qty: string
  side: TradeSide
  ts: number
}

export interface TradeSnapshot {
  symbol: string
  trades: TradeData[]
}

export interface KlineData {
  symbol: string
  interval: string
  open_time: number
  open: string
  high: string
  low: string
  close: string
  volume: string
  closed: boolean
  ts: number
}

export interface KlineSnapshot {
  symbol: string
  interval: string
  klines: KlineData[]
}

export interface ExecTrade {
  id: number
  symbol: string
  price: number
  quantity: number
  buy_order_id: number
  sell_order_id: number
  timestamp: number
}

export interface ExecReport {
  order_id: string
  status: OrderStatus
  filled: number
  leaves: number
  reason?: string
  timestamp: number
  trade?: ExecTrade
}

/** What the risk engine decided about one order or one strategy tick. */
export type RiskAction = 'ALLOW' | 'REJECT' | 'CANCEL_ONLY' | 'PAUSE' | 'HALT'

/**
 * One risk decision. REST rows carry an `id`; WebSocket events do not, so the
 * store falls back to a content key for de-duplication.
 */
export interface RiskEvent {
  id?: number
  ts: number
  account_id: number
  symbol: string
  strategy?: string
  order_id?: number
  code: string
  action: RiskAction
  message: string
}

/** Payload of `risk.snapshot`: recent history, newest first. */
export type RiskSnapshot = RiskEvent[]

/** WebSocket message types owned by the risk topic. */
export type RiskMessageType = 'risk.snapshot' | 'risk.blocked' | 'risk.event'

export interface OrderRow {
  order_id: string
  client_order_id?: string
  account_id: number
  symbol: string
  side: TradeSide
  type: string
  time_in_force: string
  price: number
  quantity: string
  status: OrderStatus
  filled: number
  leaves: number
  reason?: string
  created_at: number
  updated_at: number
}

export interface OrdersResponse {
  orders: OrderRow[]
}

export interface SubmitOrderRequest {
  order_id: string
  account_id?: number
  strategy?: string
  symbol?: string
  side: TradeSide
  price: number
  quantity: string
  type: 'LIMIT' | 'MARKET'
  time_in_force: 'GTC' | 'IOC' | 'FOK'
}

export interface SubmitOrderResponse {
  status: string
  order_id: string
  client_oid?: string
  msg?: string
}

export interface ApiError {
  code: number
  message: string
}

export interface ClientCommand {
  op: 'sub' | 'unsub' | 'ping'
  topics?: string[]
  last_seq?: number
  ts?: number
}
