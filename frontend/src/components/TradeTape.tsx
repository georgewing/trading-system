import { formatClock, formatPrice, formatQty } from '../lib/format'
import { useStore } from '../store/createStore'
import { tradesStore } from '../store/terminal'

export function TradeTape() {
  const trades = useStore(tradesStore)

  return (
    <section className="panel">
      <div className="panel-head">
        <h2>Trades</h2>
        <span className="muted">{trades.length} prints</span>
      </div>

      <div className="tape">
        <div className="tape-labels">
          <span>Price</span>
          <span>Size</span>
          <span>Time</span>
        </div>

        <div className="tape-body">
          {trades.length === 0 && <div className="empty">no prints yet…</div>}

          {trades.map((trade) => (
            <div className="tape-row" key={trade.id}>
              <span className={`px ${trade.side === 'BUY' ? 'bid' : 'ask'}`}>
                {formatPrice(trade.price)}
              </span>
              <span className="qty">{formatQty(trade.qty, 5)}</span>
              <span className="time">{formatClock(trade.ts)}</span>
            </div>
          ))}
        </div>
      </div>
    </section>
  )
}
