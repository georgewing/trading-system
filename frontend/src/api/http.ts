import type {
  ApiError,
  OrderRow,
  OrdersResponse,
  SubmitOrderRequest,
  SubmitOrderResponse,
} from './envelope'

/** Instrument metadata served by GET /api/v1/meta. */
export interface MetaResponse {
  symbol: string
  price_precision: number
  quantity_precision: number
}

export interface PositionRow {
  account_id: number
  symbol: string
  qty: string
  avg_cost: string
  mark: string
  realized: string
  unrealized: string
}

export interface PositionsResponse {
  enabled: boolean
  positions: PositionRow[]
}

export interface AccountSummary {
  account_id: number
  enabled: boolean
  realized: string
  unrealized: string
  equity: string
  open_positions: number
  closed_trades: number
  wins: number
}

/** Read instrument metadata before opening the WebSocket. */
export async function fetchMeta(): Promise<MetaResponse> {
  const res = await fetch('/api/v1/meta')
  if (!res.ok) throw new Error(await readError(res))
  return (await res.json()) as MetaResponse
}

export async function fetchPositions(accountId = 0): Promise<PositionsResponse> {
  const res = await fetch(`/api/v1/positions?account_id=${accountId}`)
  if (!res.ok) throw new Error(await readError(res))
  return (await res.json()) as PositionsResponse
}

export async function fetchAccount(accountId = 0): Promise<AccountSummary> {
  const res = await fetch(`/api/v1/account?account_id=${accountId}`)
  if (!res.ok) throw new Error(await readError(res))
  return (await res.json()) as AccountSummary
}

/** Cancel a resting order. */
export async function cancelOrder(orderId: string, reason = 'user'): Promise<void> {
  const res = await fetch('/api/v1/order/cancel', {
    method: 'POST',
    headers: { 'Content-Type': 'application/json' },
    body: JSON.stringify({ order_id: orderId, reason }),
  })
  if (!res.ok) throw new Error(await readError(res))
}

/** Read the recent order page used to seed the order panel. */
export async function fetchOrders(limit = 100): Promise<OrderRow[]> {
  const res = await fetch(`/api/v1/orders?limit=${limit}`)
  if (!res.ok) throw new Error(await readError(res))

  const body = (await res.json()) as OrdersResponse
  return body.orders ?? []
}

/** Submit an order. Price must already be integer ticks. */
export async function submitOrder(
  req: SubmitOrderRequest,
): Promise<SubmitOrderResponse> {
  const res = await fetch('/api/v1/order', {
    method: 'POST',
    headers: { 'Content-Type': 'application/json' },
    body: JSON.stringify(req),
  })
  if (!res.ok) throw new Error(await readError(res))

  return (await res.json()) as SubmitOrderResponse
}

async function readError(res: Response): Promise<string> {
  try {
    const body = (await res.json()) as ApiError
    return body.message || `HTTP ${res.status}`
  } catch {
    return `HTTP ${res.status}`
  }
}
