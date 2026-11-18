import { formatPrice } from '../lib/format'
import { useStore } from '../store/createStore'
import {
  connectionStore,
  klinesStore,
  metaStore,
  tradesStore,
} from '../store/terminal'

const STATUS_LABEL: Record<string, string> = {
  idle: 'IDLE',
  connecting: 'CONNECTING',
  open: 'LIVE',
  closed: 'RECONNECTING',
}

export function Header() {
  const meta = useStore(metaStore)
  const conn = useStore(connectionStore)
  const trades = useStore(tradesStore)
  const klines = useStore(klinesStore)

  const lastPrice = trades[0]?.price ?? klines[klines.length - 1]?.close
  const prevPrice = trades[1]?.price
  const delta =
    lastPrice && prevPrice ? Number(lastPrice) - Number(prevPrice) : 0
  const trend = delta > 0 ? 'up' : delta < 0 ? 'down' : ''

  return (
    <header className="header">
      <div className="brand">
        <span className="brand-mark">TS</span>
        <span className="brand-name">Trading Terminal</span>
      </div>

      <div className="symbol-block">
        <span className="symbol">{meta.symbol}</span>
        <span className={`last-price ${trend}`}>
          {lastPrice ? formatPrice(lastPrice) : '—'}
        </span>
      </div>

      <div className="conn">
        <span className={`dot ${conn.status}`} aria-hidden />
        <span className="conn-label">
          {STATUS_LABEL[conn.status] ?? conn.status}
        </span>
        {conn.latencyMs !== null && conn.status === 'open' && (
          <span className="conn-meta">{conn.latencyMs}ms</span>
        )}
        {conn.attempts > 0 && conn.status !== 'open' && (
          <span className="conn-meta">retry #{conn.attempts}</span>
        )}
        {conn.gaps > 0 && <span className="conn-meta warn">gaps {conn.gaps}</span>}
      </div>
    </header>
  )
}
