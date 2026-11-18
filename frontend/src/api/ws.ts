import type {
  ClientCommand,
  DepthData,
  Envelope,
  ExecReport,
  HelloPayload,
  KlineData,
  KlineSnapshot,
  TradeData,
  TradeSnapshot,
} from './envelope'
import {
  connectionStore,
  execTickStore,
  mergeKlines,
  metaStore,
  prependTrades,
  replaceKlines,
  replaceTrades,
  setDepth,
  upsertExecReport,
} from '../store/terminal'

const MIN_BACKOFF_MS = 500
const MAX_BACKOFF_MS = 15000
const PING_INTERVAL_MS = 15000
const RESYNC_COOLDOWN_MS = 1000

/**
 * Coalesced update set. One animation frame may carry dozens of depth updates
 * and hundreds of prints; React sees a single commit per frame.
 */
interface Batch {
  depth: DepthData | null
  trades: TradeData[] | null
  tradeAppend: TradeData[]
  klines: KlineData[] | null
  klineAppend: KlineData[]
  orders: ExecReport[]
  lastSeq: number
  gaps: number
}

function emptyBatch(lastSeq: number): Batch {
  return {
    depth: null,
    trades: null,
    tradeAppend: [],
    klines: null,
    klineAppend: [],
    orders: [],
    lastSeq,
    gaps: 0,
  }
}

/**
 * Reconnecting WebSocket client for the terminal.
 *
 * Responsibilities:
 *  - exponential backoff with jitter
 *  - resubscribe on every reconnect, carrying last_seq so the server replays
 *    snapshots instead of leaving a hole
 *  - sequence-gap detection with a cooldown, so a lossy link cannot turn into a
 *    resubscribe storm
 *  - rAF batching so high-frequency data never drives one render per message
 */
export class TradingSocket {
  private ws: WebSocket | null = null
  private attempts = 0
  private lastSeq = 0
  private stopped = false
  private lastResyncAt = 0

  private reconnectTimer: number | null = null
  private pingTimer: number | null = null
  private frame: number | null = null

  private pingSentAt = 0
  private batch: Batch = emptyBatch(0)

  private readonly topics: string[]

  constructor(topics: string[]) {
    this.topics = topics
  }

  start(): void {
    this.stopped = false
    this.connect()
  }

  stop(): void {
    this.stopped = true
    this.clearTimers()
    this.ws?.close()
    this.ws = null
    connectionStore.set((prev) => ({ ...prev, status: 'closed' }))
  }

  private connect(): void {
    connectionStore.set((prev) => ({ ...prev, status: 'connecting' }))

    const scheme = window.location.protocol === 'https:' ? 'wss:' : 'ws:'
    const ws = new WebSocket(`${scheme}//${window.location.host}/ws`)
    this.ws = ws

    ws.onopen = () => {
      this.attempts = 0
      connectionStore.set((prev) => ({ ...prev, status: 'open', attempts: 0 }))

      // last_seq lets the server answer with snapshots after a gap.
      this.send({ op: 'sub', topics: this.topics, last_seq: this.lastSeq })

      this.pingTimer = window.setInterval(() => this.ping(), PING_INTERVAL_MS)
    }

    ws.onmessage = (event) => this.onMessage(String(event.data))

    ws.onclose = () => {
      this.stopPing()
      if (this.stopped) return
      this.scheduleReconnect()
    }

    ws.onerror = () => ws.close()
  }

  private scheduleReconnect(): void {
    const base = Math.min(MAX_BACKOFF_MS, MIN_BACKOFF_MS * 2 ** this.attempts)
    const delay = base * (0.8 + Math.random() * 0.4)

    this.attempts += 1
    connectionStore.set((prev) => ({
      ...prev,
      status: 'closed',
      attempts: this.attempts,
    }))

    this.reconnectTimer = window.setTimeout(() => this.connect(), delay)
  }

  private ping(): void {
    if (!this.ws || this.ws.readyState !== WebSocket.OPEN) return
    this.pingSentAt = performance.now()
    this.send({ op: 'ping', ts: Date.now() })
  }

  private send(cmd: ClientCommand): void {
    if (this.ws && this.ws.readyState === WebSocket.OPEN) {
      this.ws.send(JSON.stringify(cmd))
    }
  }

  private onMessage(raw: string): void {
    let env: Envelope
    try {
      env = JSON.parse(raw) as Envelope
    } catch {
      return
    }

    if (env.type === 'system.hello') {
      const hello = env.data as HelloPayload
      metaStore.set({
        symbol: hello.symbol,
        pricePrecision: hello.price_precision,
        quantityPrecision: hello.quantity_precision,
        ready: true,
        topics: hello.topics,
      })
      this.lastSeq = env.seq
      return
    }

    if (env.type === 'system.pong') {
      if (this.pingSentAt > 0) {
        const rtt = Math.round(performance.now() - this.pingSentAt)
        this.pingSentAt = 0
        connectionStore.set((prev) => ({ ...prev, latencyMs: rtt }))
      }
      return
    }

    if (env.type === 'system.error') {
      const err = env.data as { code: string; message: string }
      if (err.code === 'resync') {
        this.requestResync()
      }
      return
    }

    if (env.seq > 0) {
      if (this.lastSeq > 0 && env.seq !== this.lastSeq + 1) {
        this.batch.gaps += 1
        this.requestResync()
      }
      this.lastSeq = env.seq
      this.batch.lastSeq = env.seq
    }

    switch (env.type) {
      case 'market.depth':
        this.batch.depth = env.data as DepthData
        break
      case 'market.trade':
        this.batch.tradeAppend.push(env.data as TradeData)
        break
      case 'market.trade.snapshot':
        this.batch.trades = (env.data as TradeSnapshot).trades
        break
      case 'market.kline':
        this.batch.klineAppend.push(env.data as KlineData)
        break
      case 'market.kline.snapshot':
        this.batch.klines = (env.data as KlineSnapshot).klines
        break
      case 'order.exec':
        this.batch.orders.push(env.data as ExecReport)
        break
      default:
        break
    }

    this.scheduleFlush()
  }

  private requestResync(): void {
    const now = performance.now()
    if (now - this.lastResyncAt < RESYNC_COOLDOWN_MS) return
    this.lastResyncAt = now
    this.send({ op: 'sub', topics: this.topics, last_seq: this.lastSeq })
  }

  private scheduleFlush(): void {
    if (this.frame !== null) return
    this.frame = window.requestAnimationFrame(() => {
      this.frame = null
      this.flush()
    })
  }

  private flush(): void {
    const batch = this.batch
    this.batch = emptyBatch(batch.lastSeq)

    if (batch.depth) setDepth(batch.depth)
    if (batch.trades) replaceTrades(batch.trades)
    if (batch.tradeAppend.length > 0) prependTrades(batch.tradeAppend)
    if (batch.klines) replaceKlines(batch.klines)
    if (batch.klineAppend.length > 0) mergeKlines(batch.klineAppend)
  	  for (const report of batch.orders) upsertExecReport(report)
  	  if (batch.orders.length > 0) execTickStore.set((n) => n + 1)

    if (batch.gaps > 0 || batch.lastSeq !== 0) {
      connectionStore.set((prev) => ({
        ...prev,
        gaps: prev.gaps + batch.gaps,
        lastSeq: batch.lastSeq || prev.lastSeq,
      }))
    }
  }

  private stopPing(): void {
    if (this.pingTimer !== null) {
      window.clearInterval(this.pingTimer)
      this.pingTimer = null
    }
  }

  private clearTimers(): void {
    this.stopPing()
    if (this.reconnectTimer !== null) {
      window.clearTimeout(this.reconnectTimer)
      this.reconnectTimer = null
    }
    if (this.frame !== null) {
      window.cancelAnimationFrame(this.frame)
      this.frame = null
    }
  }
}
