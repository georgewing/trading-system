#!/usr/bin/env node
// Verifies that a fill reaches BOTH sides of the position ledger (taker and
// maker) and therefore the portfolio endpoints.
//
// Requires a config with risk.enabled=true.
// Usage: node scripts/dev/e2e-positions.mjs [httpBase]
//
// Precision note: the config this runs against must keep
// price_precision + quantity_precision small enough that price_ticks * lots
// does not overflow int64. btc 2/6 is safe; 8/8 is not.

const httpBase = process.argv[2] ?? 'http://127.0.0.1:8091'

const MAKER = 7
const TAKER = 8

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
  const stamp = Date.now()

  const order = (id, account, side, qty) => ({
    order_id: id,
    account_id: account,
    symbol: meta.symbol,
    side,
    price,
    quantity: qty,
    type: 'LIMIT',
    time_in_force: 'GTC',
  })

  // Account 7 rests a bid for 2. Account 8 sells 1 into it: 1 lot trades and
  // 1 lot stays resting. Account 7 should end up long 1, account 8 short 1.
  let r = await post('/api/v1/order', order(`maker-${stamp}`, MAKER, 'BUY', '2'))
  if (r.status !== 202) fail(`maker buy -> ${r.status} ${JSON.stringify(r.body)}`)

  await new Promise((res) => setTimeout(res, 250))

  r = await post('/api/v1/order', order(`taker-${stamp}`, TAKER, 'SELL', '1'))
  if (r.status !== 202) fail(`taker sell -> ${r.status} ${JSON.stringify(r.body)}`)

  await new Promise((res) => setTimeout(res, 400))

  const summary = await getJSON('/api/v1/positions')
  if (!summary.enabled) fail('positions disabled, want enabled')

  const maker = summary.positions.find(
    (p) => p.account_id === MAKER && p.symbol === meta.symbol,
  )
  const taker = summary.positions.find(
    (p) => p.account_id === TAKER && p.symbol === meta.symbol,
  )

  console.log(`maker: ${maker ? `qty=${maker.qty} avg=${maker.avg_cost}` : 'missing'}`)
  console.log(`taker: ${taker ? `qty=${taker.qty} avg=${taker.avg_cost}` : 'missing'}`)

  if (!maker) fail('maker position missing: resting fills are not reported')
  if (Number(maker.qty) !== 1) fail(`maker qty = ${maker.qty}, want 1`)
  if (!taker) fail('taker position missing')
  if (Number(taker.qty) !== -1) fail(`taker qty = ${taker.qty}, want -1`)

  const account = await getJSON(`/api/v1/account?account_id=${MAKER}`)
  console.log(`maker account: open=${account.open_positions} equity=${account.equity}`)
  if (account.open_positions !== 1) {
    fail(`open_positions = ${account.open_positions}, want 1`)
  }

  console.log('PASS: fills reach both sides of the ledger')
}

main().catch((err) => fail(err instanceof Error ? err.message : String(err)))
