#!/usr/bin/env node
// Runtime check for batch cancel and the history endpoint.
//
// Usage: node scripts/dev/e2e-batch.mjs [httpBase]

const httpBase = process.argv[2] ?? 'http://127.0.0.1:8091'

const fail = (msg) => {
  console.error(`FAIL: ${msg}`)
  process.exit(1)
}

const post = async (path, body) => {
  const res = await fetch(`${httpBase}${path}`, {
    method: 'POST',
    headers: { 'Content-Type': 'application/json' },
    body: JSON.stringify(body),
  })
  return { status: res.status, body: await res.json().catch(() => ({})) }
}

const getJSON = async (path) => {
  const res = await fetch(`${httpBase}${path}`)
  return { status: res.status, body: await res.json().catch(() => ({})) }
}

const main = async () => {
  const meta = (await getJSON('/api/v1/meta')).body
  const base = Math.round(60000 * 10 ** meta.price_precision)
  const stamp = Date.now()
  const account = 11

  const place = (i, price) =>
    post('/api/v1/order', {
      order_id: `batch-${stamp}-${i}`,
      account_id: account,
      symbol: meta.symbol,
      side: 'BUY',
      price,
      quantity: '1',
      type: 'LIMIT',
      time_in_force: 'GTC',
    })

  // Two distinct prices so both orders rest independently.
  const a = await place(1, base)
  const b = await place(2, base - 1)
  if (a.status !== 202 || b.status !== 202) {
    fail(`submit -> ${a.status}/${b.status}`)
  }
  console.log(`accepted: ${a.body.order_id} ${b.body.order_id}`)

  await new Promise((r) => setTimeout(r, 300))

  const batch = await post('/api/v1/order/cancel/batch', {
    order_ids: [a.body.order_id, b.body.order_id],
    reason: 'batch-e2e',
  })
  if (batch.status !== 200) fail(`batch -> ${batch.status} ${JSON.stringify(batch.body)}`)
  if (batch.body.canceled.length !== 2) {
    fail(`canceled ${batch.body.canceled.length}, want 2 (${JSON.stringify(batch.body.failed)})`)
  }
  console.log(`batch canceled: ${batch.body.canceled.length}`)

  const { orders } = (await getJSON('/api/v1/orders?limit=20')).body
  for (const id of [a.body.order_id, b.body.order_id]) {
    const row = orders.find((o) => o.order_id === id)
    if (!row || row.status !== 'CANCELED') {
      fail(`order ${id} status = ${row ? row.status : 'missing'}`)
    }
  }
  console.log('order statuses: CANCELED x2')

  // Empty list must be rejected, not treated as "cancel nothing successfully".
  const empty = await post('/api/v1/order/cancel/batch', { order_ids: [] })
  if (empty.status !== 400) fail(`empty batch -> ${empty.status}, want 400`)
  console.log('empty batch: 400 (expected)')

  // ClickHouse is not enabled in this config, so history must say so.
  const history = await getJSON(
    `/api/v1/market/kline/history?symbol=${meta.symbol}&limit=5`,
  )
  if (history.status !== 503) fail(`history -> ${history.status}, want 503`)
  console.log(`history: 503 "${history.body.message}"`)

  console.log('PASS: batch cancel + history endpoint')
}

main().catch((err) => fail(err instanceof Error ? err.message : String(err)))
