#!/usr/bin/env node
// End-to-end check for the cancel path and the portfolio endpoints.
//
// Usage: node scripts/dev/e2e-cancel.mjs [httpBase]

const httpBase = process.argv[2] ?? 'http://127.0.0.1:8090'

const fail = (msg) => {
  console.error(`FAIL: ${msg}`)
  process.exit(1)
}

const getJSON = async (path) => {
  const res = await fetch(`${httpBase}${path}`)
  if (!res.ok) fail(`${path} -> ${res.status} ${await res.text()}`)
  return res.json()
}

const main = async () => {
  const meta = await getJSON('/api/v1/meta')
  const price = Math.round(60000.01 * 10 ** meta.price_precision)

  // 1. Submit a resting order.
  const submitRes = await fetch(`${httpBase}/api/v1/order`, {
    method: 'POST',
    headers: { 'Content-Type': 'application/json' },
    body: JSON.stringify({
      order_id: `cancel-${Date.now()}`,
      symbol: meta.symbol,
      side: 'BUY',
      price,
      quantity: '0.001',
      type: 'LIMIT',
      time_in_force: 'GTC',
    }),
  })
  if (submitRes.status !== 202) {
    fail(`submit -> ${submitRes.status} ${await submitRes.text()}`)
  }
  const ack = await submitRes.json()
  console.log(`accepted: ${ack.order_id}`)

  // Give the pipeline a moment to rest the order.
  await new Promise((r) => setTimeout(r, 300))

  // 2. Cancel it.
  const cancelRes = await fetch(`${httpBase}/api/v1/order/cancel`, {
    method: 'POST',
    headers: { 'Content-Type': 'application/json' },
    body: JSON.stringify({ order_id: ack.order_id, reason: 'e2e' }),
  })
  if (cancelRes.status !== 200) {
    fail(`cancel -> ${cancelRes.status} ${await cancelRes.text()}`)
  }
  console.log(`canceled: ${JSON.stringify(await cancelRes.json())}`)

  // 3. The order list must reflect CANCELED.
  const { orders } = await getJSON('/api/v1/orders?limit=20')
  const target = orders.find((o) => o.order_id === ack.order_id)
  if (!target) fail('order not present in /api/v1/orders')
  if (target.status !== 'CANCELED') fail(`status = ${target.status}, want CANCELED`)
  console.log(`order status: ${target.status}`)

  // 4. Cancelling again must be a clean 404, not a silent success.
  const again = await fetch(`${httpBase}/api/v1/order/cancel`, {
    method: 'POST',
    headers: { 'Content-Type': 'application/json' },
    body: JSON.stringify({ order_id: ack.order_id }),
  })
  if (again.status !== 404) fail(`second cancel -> ${again.status}, want 404`)
  console.log('second cancel: 404 (expected)')

  // 5. Portfolio endpoints must answer.
  const positions = await getJSON('/api/v1/positions')
  if (!Array.isArray(positions.positions)) fail('positions payload malformed')
  console.log(`positions: enabled=${positions.enabled} count=${positions.positions.length}`)

  const account = await getJSON('/api/v1/account')
  if (typeof account.equity !== 'string') fail('account payload malformed')
  console.log(`account: equity=${account.equity} closed=${account.closed_trades}`)

  console.log('PASS: cancel + portfolio endpoints')
}

main().catch((err) => fail(err instanceof Error ? err.message : String(err)))
