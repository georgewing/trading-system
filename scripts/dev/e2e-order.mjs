#!/usr/bin/env node
// End-to-end check: connect to /ws, submit an order over HTTP and wait for the
// matching execution report to come back on the same socket.
//
// Usage: node scripts/dev/e2e-order.mjs [httpBase] [wsUrl]

const httpBase = process.argv[2] ?? 'http://127.0.0.1:5173'
const wsUrl = process.argv[3] ?? 'ws://127.0.0.1:5173/ws'

const ws = new WebSocket(wsUrl)
let settled = false

const fail = (msg) => {
  if (settled) return
  settled = true
  console.error(`FAIL: ${msg}`)
  process.exit(1)
}

const finish = (msg) => {
  if (settled) return
  settled = true
  console.log(`PASS: ${msg}`)
  ws.close()
  process.exit(0)
}

const deadline = setTimeout(() => fail('no order.exec within 8s'), 8000)

ws.onerror = () => fail('websocket error')

ws.onopen = () => {
  ws.send(JSON.stringify({ op: 'sub', topics: ['order'] }))
}

ws.onmessage = async (event) => {
  const msg = JSON.parse(event.data)

  if (msg.type === 'system.hello') {
    const meta = await fetch(`${httpBase}/api/v1/meta`).then((r) => r.json())
    const symbol = meta.symbol
    const price = Math.round(62762.01 * 10 ** meta.price_precision)

    console.log(`hello: ${symbol} precision=${meta.price_precision}`)

    const res = await fetch(`${httpBase}/api/v1/order`, {
      method: 'POST',
      headers: { 'Content-Type': 'application/json' },
      body: JSON.stringify({
        order_id: `e2e-${Date.now()}`,
        symbol,
        side: 'BUY',
        price,
        quantity: '0.001',
        type: 'LIMIT',
        time_in_force: 'GTC',
      }),
    })

    if (res.status !== 202) {
      fail(`order submit returned ${res.status}`)
      return
    }
    const ack = await res.json()
    console.log(`accepted: ${ack.order_id}`)
    return
  }

  if (msg.type === 'order.exec') {
    clearTimeout(deadline)
    console.log(`report: order=${msg.data.order_id} status=${msg.data.status}`)
    finish(`order.exec received with status ${msg.data.status}`)
  }
}
