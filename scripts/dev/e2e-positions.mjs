#!/usr/bin/env node
// Verifies that fills reach the position ledger and therefore the portfolio
// endpoints. Requires a config with risk.enabled=true.
//
// Usage: node scripts/dev/e2e-positions.mjs [httpBase]

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
  if (!res.ok) fail(`${path} -> ${res.status} ${await res.text()}`)
  return res.json()
}

const main = async () => {
  const meta = await getJSON('/api/v1/meta')
  const price = Math.round(60000 * 10 ** meta.price_precision)
  const account = 7
  const stamp = Date.now()

  const order = (id, side, qty) => ({
    order_id: id,
    account_id: account,
    symbol: meta.symbol,
    side,
    price,
    quantity: qty,
    type: 'LIMIT',
    time_in_force: 'GTC',
  })

  // Buy 2 rests, then sell 1 crosses: one lot fills, one stays open.
  let r = await post('/api/v1/order', order(`pos-a-${stamp}`, 'BUY', '2'))
  if (r.status !== 202) fail(`buy -> ${r.status}`)

  await new Promise((res) => setTimeout(res, 250))

  r = await post('/api/v1/order', order(`pos-b-${stamp}`, 'SELL', '1'))
  if (r.status !== 202) fail(`sell -> ${r.status}`)

  await new Promise((res) => setTimeout(res, 400))

  const positions = await getJSON(`/api/v1/positions?account_id=${account}`)
  if (!positions.enabled) fail('positions disabled, want enabled')

  const row = positions.positions.find((p) => p.symbol === meta.symbol)
  if (!row) fail('no position row after fill')

  console.log(`position: qty=${row.qty} avg=${row.avg_cost} mark=${row.mark}`)
  if (Number(row.qty) !== 1) fail(`qty = ${row.qty}, want 1`)

  const accountSummary = await getJSON(`/api/v1/account?account_id=${account}`)
  console.log(
    `account: open=${accountSummary.open_positions} equity=${accountSummary.equity}`,
  )
  if (accountSummary.open_positions !== 1) {
    fail(`open_positions = ${accountSummary.open_positions}, want 1`)
  }

  console.log('PASS: fills reach the position ledger')
}

main().catch((err) => fail(err instanceof Error ? err.message : String(err)))
