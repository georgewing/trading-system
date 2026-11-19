import type { ApiError, KlineData } from './envelope'

/** Query for GET /api/v1/market/kline/history. */
export interface KlineHistoryQuery {
  symbol: string
  /** Inclusive lower bound in unix milliseconds. Omitted means unbounded. */
  fromMs?: number
  /** Inclusive upper bound in unix milliseconds. Omitted means unbounded. */
  toMs?: number
  /** Newest N candles. The backend defaults to 300 and clamps at 1000. */
  limit?: number
}

interface KlineHistoryResponse {
  symbol: string
  interval: string
  klines: KlineData[]
}

/** Only interval the kline_1m view can answer. */
const HISTORY_INTERVAL = '1m'

/**
 * Read stored 1m candles. A non-2xx response throws the backend message so the
 * panel can show it; an empty range is a normal empty array, not an error.
 */
export async function fetchKlineHistory(
  query: KlineHistoryQuery,
): Promise<KlineData[]> {
  const params = new URLSearchParams({
    symbol: query.symbol,
    interval: HISTORY_INTERVAL,
  })

  // The backend treats a non-positive bound as open-ended, but its HTTP layer
  // rejects negative values, so leave both out instead of sending them.
  if (query.fromMs !== undefined && query.fromMs > 0) {
    params.set('from', String(Math.trunc(query.fromMs)))
  }
  if (query.toMs !== undefined && query.toMs > 0) {
    params.set('to', String(Math.trunc(query.toMs)))
  }
  if (query.limit !== undefined && query.limit > 0) {
    params.set('limit', String(Math.trunc(query.limit)))
  }

  const res = await fetch(
    `/api/v1/market/kline/history?${params.toString()}`,
  )
  if (!res.ok) throw new Error(await readError(res))

  const body = (await res.json()) as KlineHistoryResponse
  return body.klines ?? []
}

async function readError(res: Response): Promise<string> {
  try {
    const body = (await res.json()) as ApiError
    return body.message || `HTTP ${res.status}`
  } catch {
    return `HTTP ${res.status}`
  }
}
