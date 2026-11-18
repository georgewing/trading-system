import { formatPrice, formatQty } from '../lib/format'
import { useStore } from '../store/createStore'
import { depthStore } from '../store/terminal'

const LEVELS = 15

export function OrderBook() {
  const depth = useStore(depthStore)

  if (!depth) {
    return (
      <section className="panel">
        <div className="panel-head">
          <h2>Order Book</h2>
        </div>
        <div className="empty">waiting for feed…</div>
      </section>
    )
  }

  const asks = depth.asks.slice(0, LEVELS).reverse()
  const bids = depth.bids.slice(0, LEVELS)

  const maxQty = Math.max(
    1,
    ...asks.map(([, q]) => Number(q)),
    ...bids.map(([, q]) => Number(q)),
  )

  const bestAsk = depth.asks[0]?.[0]
  const bestBid = depth.bids[0]?.[0]
  const spread =
    bestAsk && bestBid ? (Number(bestAsk) - Number(bestBid)).toFixed(2) : null

  return (
    <section className="panel">
      <div className="panel-head">
        <h2>Order Book</h2>
        {spread !== null && <span className="muted">spread {spread}</span>}
      </div>

      <div className="book">
        <div className="book-labels">
          <span>Price</span>
          <span>Size</span>
        </div>

        <div className="book-side asks">
          {asks.map(([price, qty]) => {
            const width = (Number(qty) / maxQty) * 100
            return (
              <div className="book-row" key={`a-${price}`}>
                <span className="bar bar-ask" style={{ width: `${width}%` }} />
                <span className="px ask">{formatPrice(price)}</span>
                <span className="qty">{formatQty(qty)}</span>
              </div>
            )
          })}
        </div>

        <div className="book-mid">
          <span className="mid-price">{bestAsk ? formatPrice(bestAsk) : '—'}</span>
          <span className="muted">last update {depth.last_update_id}</span>
        </div>

        <div className="book-side bids">
          {bids.map(([price, qty]) => {
            const width = (Number(qty) / maxQty) * 100
            return (
              <div className="book-row" key={`b-${price}`}>
                <span className="bar bar-bid" style={{ width: `${width}%` }} />
                <span className="px bid">{formatPrice(price)}</span>
                <span className="qty">{formatQty(qty)}</span>
              </div>
            )
          })}
        </div>
      </div>
    </section>
  )
}
