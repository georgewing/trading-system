#!/usr/bin/env node
// Dev probe for the /ws endpoint: connects, subscribes to a few topics and
// prints one line per message type so the feed can be verified without a UI.
//
// Usage: node scripts/dev/ws-probe.mjs [ws://127.0.0.1:8090/ws] [symbol]

const url = process.argv[2] ?? 'ws://127.0.0.1:8090/ws'
const symbol = process.argv[3] ?? 'BTCUSDT'

const seen = new Map()
const ws = new WebSocket(url)

const timer = setTimeout(() => {
  console.log('--- summary ---')
  for (const [type, count] of [...seen.entries()].sort()) {
    console.log(`${type.padEnd(24)} ${count}`)
  }
  ws.close()
  process.exit(seen.size >= 3 ? 0 : 1)
}, 8000)

ws.onopen = () => {
  console.log(`connected: ${url}`)
  ws.send(JSON.stringify({
    op: 'sub',
    topics: [`depth:${symbol}`, `trade:${symbol}`, `kline:${symbol}:1m`, 'order'],
  }))
}

ws.onmessage = (event) => {
  const msg = JSON.parse(event.data)
  const count = (seen.get(msg.type) ?? 0) + 1
  seen.set(msg.type, count)

  if (count <= 2) {
    const data = JSON.stringify(msg.data)
    console.log(`seq=${msg.seq} type=${msg.type} ${data.slice(0, 160)}`)
  }
}

ws.onerror = (err) => {
  console.error('ws error:', err.message ?? err)
}

ws.onclose = () => {
  clearTimeout(timer)
}
