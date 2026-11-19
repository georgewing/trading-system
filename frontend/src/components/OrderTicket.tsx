import { useEffect, useState, type FormEvent } from 'react'
import { submitOrder } from '../api/http'
import { toTicks } from '../lib/format'
import { useStore } from '../store/createStore'
import { metaStore, tradesStore } from '../store/terminal'

type Side = 'BUY' | 'SELL'
type OrderType = 'LIMIT' | 'MARKET'
type TIF = 'GTC' | 'IOC' | 'FOK'

interface Notice {
  kind: 'ok' | 'err'
  text: string
}

/** Strip grouping and trailing zeros so the value fits an <input>. */
function plain(raw: string): string {
  const n = Number(raw)
  if (!Number.isFinite(n)) return ''
  return String(n)
}

export function OrderTicket() {
  const meta = useStore(metaStore)
  const trades = useStore(tradesStore)

  	const [side, setSide] = useState<Side>('BUY')
  	const [type, setType] = useState<OrderType>('LIMIT')
  	const [tif, setTif] = useState<TIF>('GTC')
  	const [accountId, setAccountId] = useState('1')
  	const [strategy, setStrategy] = useState('manual')
  	const [price, setPrice] = useState('')
  	const [quantity, setQuantity] = useState('0.001')
  const [busy, setBusy] = useState(false)
  const [notice, setNotice] = useState<Notice | null>(null)

  const lastPrice = trades[0]?.price

  // Seed the price input once, then leave it alone so typing is not clobbered.
  useEffect(() => {
    if (!lastPrice) return
    setPrice((current) => (current === '' ? plain(lastPrice) : current))
  }, [lastPrice])

  const ticks = toTicks(price, meta.pricePrecision)

  async function handleSubmit(event: FormEvent) {
    event.preventDefault()
    setNotice(null)

    if (ticks === null) {
      setNotice({ kind: 'err', text: 'price must be a positive number' })
      return
    }
    if (!quantity || Number(quantity) <= 0) {
      setNotice({ kind: 'err', text: 'quantity must be positive' })
      return
    }

    setBusy(true)
    try {
      			const res = await submitOrder({
        				order_id: `web-${Date.now()}`,
        				account_id: Number(accountId) || 0,
        				strategy: strategy.trim() || undefined,
        				symbol: meta.symbol,
        side,
        price: ticks,
        quantity,
        type,
        time_in_force: tif,
      })
      setNotice({ kind: 'ok', text: `accepted · ${res.order_id}` })
    } catch (err) {
      setNotice({
        kind: 'err',
        text: err instanceof Error ? err.message : 'submit failed',
      })
    } finally {
      setBusy(false)
    }
  }

  return (
    <section className="panel panel-ticket">
      <div className="panel-head">
        <h2>Order</h2>
        <span className="muted">{meta.symbol}</span>
      </div>

      <form className="ticket" onSubmit={handleSubmit}>
        <div className="side-switch">
          <button
            type="button"
            className={`side-btn buy ${side === 'BUY' ? 'active' : ''}`}
            onClick={() => setSide('BUY')}
          >
            Buy
          </button>
          <button
            type="button"
            className={`side-btn sell ${side === 'SELL' ? 'active' : ''}`}
            onClick={() => setSide('SELL')}
          >
            Sell
          </button>
        </div>

        		<label className="field">
        		  <span>Account</span>
        		  <input
        			value={accountId}
        			onChange={(e) => setAccountId(e.target.value)}
        			inputMode="numeric"
        			placeholder="1"
        		  />
        		</label>

        		<label className="field">
        		  <span>Strategy</span>
        		  <input
        			value={strategy}
        			onChange={(e) => setStrategy(e.target.value)}
        			placeholder="manual"
        		  />
        		</label>

        		<label className="field">
        		  <span>Type</span>
          <select
            value={type}
            onChange={(e) => setType(e.target.value as OrderType)}
          >
            <option value="LIMIT">LIMIT</option>
            <option value="MARKET">MARKET</option>
          </select>
        </label>

        <label className="field">
          <span>Price</span>
          <input
            value={price}
            onChange={(e) => setPrice(e.target.value)}
            inputMode="decimal"
            placeholder="0.00"
          />
        </label>

        <label className="field">
          <span>Quantity</span>
          <input
            value={quantity}
            onChange={(e) => setQuantity(e.target.value)}
            inputMode="decimal"
            placeholder="0.001"
          />
        </label>

        <label className="field">
          <span>Time in force</span>
          <select value={tif} onChange={(e) => setTif(e.target.value as TIF)}>
            <option value="GTC">GTC</option>
            <option value="IOC">IOC</option>
            <option value="FOK">FOK</option>
          </select>
        </label>

        <p className="hint">
          sends {ticks ?? '—'} ticks · precision {meta.pricePrecision}
        </p>

        <button
          className={`submit ${side === 'BUY' ? 'buy' : 'sell'}`}
          disabled={busy}
          type="submit"
        >
          {busy ? 'Submitting…' : `${side === 'BUY' ? 'Buy' : 'Sell'} ${meta.symbol}`}
        </button>

        {notice && <p className={`notice ${notice.kind}`}>{notice.text}</p>}
      </form>
    </section>
  )
}
