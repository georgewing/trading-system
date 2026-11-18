import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'

import type { DepthData, Envelope } from './envelope'
import { TradingSocket } from './ws'
import {
  connectionStore,
  depthStore,
  klinesStore,
  metaStore,
  ordersStore,
  tradesStore,
} from '../store/terminal'

type SentCommand = Record<string, unknown>

/** Minimal browser WebSocket stand-in driven manually by the tests. */
class FakeWebSocket {
  static readonly CONNECTING = 0
  static readonly OPEN = 1
  static readonly CLOSING = 2
  static readonly CLOSED = 3

  static instances: FakeWebSocket[] = []

  readonly url: string
  readyState: number = FakeWebSocket.CONNECTING
  sent: string[] = []

  onopen: (() => void) | null = null
  onmessage: ((event: { data: string }) => void) | null = null
  onclose: (() => void) | null = null
  onerror: (() => void) | null = null

  constructor(url: string | URL) {
    this.url = String(url)
    FakeWebSocket.instances.push(this)
  }

  send(data: string): void {
    if (this.readyState !== FakeWebSocket.OPEN) {
      throw new Error('FakeWebSocket.send called while not OPEN')
    }
    this.sent.push(data)
  }

  close(): void {
    if (this.readyState === FakeWebSocket.CLOSED) return
    this.readyState = FakeWebSocket.CLOSED
    this.onclose?.()
  }

  open(): void {
    this.readyState = FakeWebSocket.OPEN
    this.onopen?.()
  }

  receive(payload: Envelope): void {
    this.onmessage?.({ data: JSON.stringify(payload) })
  }

  sentCommands(): SentCommand[] {
    return this.sent.map((raw) => JSON.parse(raw) as SentCommand)
  }

  subCommands(): SentCommand[] {
    return this.sentCommands().filter((cmd) => cmd.op === 'sub')
  }
}

interface PendingFrame {
  id: number
  callback: FrameRequestCallback
}

let clockNow = 0
let rafQueue: PendingFrame[] = []
let rafSeq = 0
let sockets: TradingSocket[] = []

const nativeRaf = window.requestAnimationFrame
const nativeCancelRaf = window.cancelAnimationFrame

/** Advance both fake timers and the clock driving performance.now(). */
function advance(ms: number): void {
  clockNow += ms
  vi.advanceTimersByTime(ms)
}

/** Run the callbacks the socket queued through requestAnimationFrame. */
function flushFrame(): void {
  const pending = rafQueue
  rafQueue = []
  for (const frame of pending) frame.callback(clockNow)
}

function startSocket(topics: string[]): TradingSocket {
  const socket = new TradingSocket(topics)
  sockets.push(socket)
  socket.start()
  return socket
}

function socketAt(index: number): FakeWebSocket {
  const ws = FakeWebSocket.instances[index]
  if (!ws) throw new Error(`FakeWebSocket #${index} was not created`)
  return ws
}

function envelope(type: string, seq: number, data: unknown): Envelope {
  return { v: 1, seq, ts: seq, type, data }
}

function depthData(updateId: number, bid: string): DepthData {
  return {
    symbol: 'BTCUSDT',
    last_update_id: updateId,
    bids: [[bid, '1.00']],
    asks: [],
    ts: updateId,
  }
}

beforeEach(() => {
  clockNow = 0
  rafQueue = []
  rafSeq = 0
  sockets = []
  FakeWebSocket.instances = []

  vi.useFakeTimers({
    toFake: ['setTimeout', 'clearTimeout', 'setInterval', 'clearInterval', 'Date'],
  })
  vi.spyOn(performance, 'now').mockImplementation(() => clockNow)
  vi.stubGlobal('WebSocket', FakeWebSocket)

  // Deterministic animation frame queue: fake timers alone cannot distinguish
  // "scheduled" from "flushed", so tests advance the queue explicitly.
  window.requestAnimationFrame = (callback: FrameRequestCallback): number => {
    rafSeq += 1
    rafQueue.push({ id: rafSeq, callback })
    return rafSeq
  }
  window.cancelAnimationFrame = (handle: number): void => {
    rafQueue = rafQueue.filter((frame) => frame.id !== handle)
  }

  connectionStore.set({
    status: 'idle',
    attempts: 0,
    latencyMs: null,
    gaps: 0,
    lastSeq: 0,
  })
  metaStore.set({
    symbol: 'BTCUSDT',
    pricePrecision: 8,
    quantityPrecision: 8,
    ready: false,
    topics: [],
  })
  depthStore.set(null)
  tradesStore.set([])
  klinesStore.set([])
  ordersStore.set([])
})

afterEach(() => {
  for (const socket of sockets) socket.stop()
  sockets = []
  FakeWebSocket.instances = []
  rafQueue = []

  window.requestAnimationFrame = nativeRaf
  window.cancelAnimationFrame = nativeCancelRaf
  vi.useRealTimers()
  vi.unstubAllGlobals()
})

describe('TradingSocket reconnect', () => {
  it('reconnects after the backoff delay and doubles it on consecutive failures', () => {
    vi.spyOn(Math, 'random').mockReturnValue(0.5)

    startSocket(['market.depth'])
    expect(FakeWebSocket.instances).toHaveLength(1)

    const first = socketAt(0)
    expect(first.url).toBe('ws://localhost:3000/ws')

    first.open()
    expect(connectionStore.get().status).toBe('open')

    first.close()
    expect(connectionStore.get().status).toBe('closed')
    expect(connectionStore.get().attempts).toBe(1)

    // Attempt 0: 500ms base, jitter factor 1.0 at random() = 0.5.
    advance(499)
    expect(FakeWebSocket.instances).toHaveLength(1)
    advance(1)
    expect(FakeWebSocket.instances).toHaveLength(2)

    const second = socketAt(1)
    expect(second.readyState).toBe(FakeWebSocket.CONNECTING)

    // The retry never opened, so the next delay doubles to 1000ms.
    second.close()
    expect(connectionStore.get().attempts).toBe(2)

    advance(999)
    expect(FakeWebSocket.instances).toHaveLength(2)
    advance(1)
    expect(FakeWebSocket.instances).toHaveLength(3)
  })

  it('resubscribes with the last seen sequence after reconnecting', () => {
    vi.spyOn(Math, 'random').mockReturnValue(0.5)

    startSocket(['market.depth', 'market.trade'])
    const first = socketAt(0)
    first.open()

    first.receive(envelope('market.depth', 5, depthData(5, '100.00')))
    first.close()

    advance(500)
    expect(FakeWebSocket.instances).toHaveLength(2)

    const second = socketAt(1)
    second.open()
    expect(connectionStore.get().attempts).toBe(0)
    expect(second.sentCommands()).toEqual([
      { op: 'sub', topics: ['market.depth', 'market.trade'], last_seq: 5 },
    ])
  })
})

describe('TradingSocket sequence gaps', () => {
  it('resubscribes on a gap and throttles resyncs for one second', () => {
    startSocket(['market.depth'])
    const ws = socketAt(0)
    ws.open()

    // The implementation seeds lastResyncAt with 0; move past that initial
    // window so the first real gap is allowed to resync.
    advance(1500)

    ws.receive(envelope('market.depth', 1, depthData(1, '100.00')))
    expect(ws.subCommands()).toHaveLength(1)

    // 1 -> 3 is a gap, so a resync sub carries the last contiguous seq.
    ws.receive(envelope('market.depth', 3, depthData(3, '101.00')))
    expect(ws.subCommands()).toHaveLength(2)
    expect(ws.subCommands()[1]).toEqual({
      op: 'sub',
      topics: ['market.depth'],
      last_seq: 1,
    })

    // Inside the cooldown the gap is still counted, but no new sub is sent.
    ws.receive(envelope('market.depth', 5, depthData(5, '102.00')))
    expect(ws.subCommands()).toHaveLength(2)

    advance(999)
    ws.receive(envelope('market.depth', 7, depthData(7, '103.00')))
    expect(ws.subCommands()).toHaveLength(2)

    // 1001ms after the last resync the cooldown has expired.
    advance(2)
    ws.receive(envelope('market.depth', 9, depthData(9, '104.00')))
    expect(ws.subCommands()).toHaveLength(3)
    expect(ws.subCommands()[2]).toEqual({
      op: 'sub',
      topics: ['market.depth'],
      last_seq: 7,
    })

    // All four gaps were batched; a single manual flush applies the batch.
    expect(connectionStore.get().gaps).toBe(0)
    flushFrame()
    expect(connectionStore.get().gaps).toBe(4)
    expect(connectionStore.get().lastSeq).toBe(9)
    expect(depthStore.get()?.last_update_id).toBe(9)
  })
})
