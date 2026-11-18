import { useEffect } from 'react'
import { fetchRiskEvents } from '../api/http'
import { formatClock } from '../lib/format'
import { riskActionClass, riskEventKey } from '../lib/risk'
import { useStore } from '../store/createStore'
import { appendRiskEvents, riskEventStore } from '../store/terminal'

const SEED_LIMIT = 50

export function RiskPanel() {
  const { events } = useStore(riskEventStore)

  // Seed with recent history once. The endpoint may still be under
  // construction, so a failure just leaves the empty state in place.
  useEffect(() => {
    let cancelled = false

    fetchRiskEvents(SEED_LIMIT)
      .then((rows) => {
        if (!cancelled) appendRiskEvents(rows)
      })
      .catch(() => {
        // No risk feed yet: the panel stays empty instead of surfacing noise.
      })

    return () => {
      cancelled = true
    }
  }, [])

  return (
    <section className="panel panel-risk">
      <div className="panel-head">
        <h2>Risk</h2>
        <span className="muted">{events.length}</span>
      </div>

      <div className="risk">
        {events.length === 0 && <div className="empty">no risk events</div>}

        {events.length > 0 && (
          <div className="risk-body">
            {events.map((event, index) => (
              <div className="risk-row" key={`${riskEventKey(event)}#${index}`}>
                <span className="risk-time">{formatClock(event.ts)}</span>
                <div className="risk-main">
                  <div className="risk-line">
                    <span className="risk-symbol">{event.symbol || '—'}</span>
                    <span className="risk-strategy muted">
                      {event.strategy || 'default'}
                    </span>
                    <span className="risk-code">{event.code}</span>
                    <span className={`risk-badge ${riskActionClass(event.action)}`}>
                      {event.action}
                    </span>
                  </div>
                  {event.message && (
                    <p className="risk-message">{event.message}</p>
                  )}
                </div>
              </div>
            ))}
          </div>
        )}
      </div>
    </section>
  )
}
