import { useEffect, useState } from 'react'
import { fetchPositions, type PositionsResponse } from '../api/http'
import { useStore } from '../store/createStore'
import { execTickStore } from '../store/terminal'

export function PositionsPanel() {
  const tick = useStore(execTickStore)
  const [data, setData] = useState<PositionsResponse | null>(null)
  const [error, setError] = useState<string | null>(null)

  // Refetch when an execution report lands: that is the only event that can
  // change a position, so no polling is needed.
  useEffect(() => {
    let cancelled = false

    fetchPositions()
      .then((res) => {
        if (cancelled) return
        setData(res)
        setError(null)
      })
      .catch((err: unknown) => {
        if (cancelled) return
        setError(err instanceof Error ? err.message : 'load failed')
      })

    return () => {
      cancelled = true
    }
  }, [tick])

  const rows = data?.positions ?? []

  return (
    <section className="panel panel-positions">
      <div className="panel-head">
        <h2>Positions</h2>
        <span className="muted">{rows.length}</span>
      </div>

      {data && !data.enabled && (
        <div className="empty">
          risk disabled — positions are only tracked when risk is enabled
        </div>
      )}
      {error && <div className="empty err">{error}</div>}

      {data?.enabled && rows.length === 0 && !error && (
        <div className="empty">no open positions</div>
      )}

      {rows.length > 0 && (
        <div className="positions">
          <div className="positions-labels">
            <span>Symbol</span>
            <span>Qty</span>
            <span>Avg</span>
            <span>Mark</span>
            <span>PnL</span>
          </div>
          <div className="positions-body">
            {rows.map((row) => {
              const pnl = Number(row.unrealized)
              return (
                <div className="positions-row" key={`${row.account_id}-${row.symbol}`}>
                  <span>{row.symbol}</span>
                  <span className={Number(row.qty) >= 0 ? 'px bid' : 'px ask'}>
                    {row.qty}
                  </span>
                  <span className="qty">{row.avg_cost}</span>
                  <span className="qty">{row.mark}</span>
                  <span className={`qty ${pnl >= 0 ? 'pnl-up' : 'pnl-down'}`}>
                    {row.unrealized}
                  </span>
                </div>
              )
            })}
          </div>
        </div>
      )}
    </section>
  )
}
