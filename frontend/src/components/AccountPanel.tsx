import { useEffect, useState } from 'react'
import { fetchAccount, type AccountSummary } from '../api/http'
import { useStore } from '../store/createStore'
import { execTickStore } from '../store/terminal'

export function AccountPanel() {
  const tick = useStore(execTickStore)
  const [account, setAccount] = useState<AccountSummary | null>(null)
  const [error, setError] = useState<string | null>(null)

  useEffect(() => {
    let cancelled = false

    fetchAccount()
      .then((res) => {
        if (cancelled) return
        setAccount(res)
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

  // The summary is derived from the position ledger only: realized plus
  // unrealized PnL. Cash and margin need a product decision, so they are not
  // invented here.
  const equity = Number(account?.equity ?? 0)

  return (
    <section className="panel panel-account">
      <div className="panel-head">
        <h2>Account</h2>
        <span className="muted">
          {account ? `#${account.account_id || 'all'}` : '—'}
        </span>
      </div>

      {error && <div className="empty err">{error}</div>}

      {!error && account && (
        <div className="account">
          <div className="account-row">
            <span>Equity (PnL)</span>
            <span className={`account-value ${equity >= 0 ? 'pnl-up' : 'pnl-down'}`}>
              {account.equity}
            </span>
          </div>
          <div className="account-row">
            <span>Realized</span>
            <span className="account-value">{account.realized}</span>
          </div>
          <div className="account-row">
            <span>Unrealized</span>
            <span className="account-value">{account.unrealized}</span>
          </div>
          <div className="account-row">
            <span>Open / closed</span>
            <span className="account-value">
              {account.open_positions} / {account.closed_trades}
            </span>
          </div>
          <div className="account-row">
            <span>Win rate</span>
            <span className="account-value">
              {account.closed_trades > 0
                ? `${Math.round((account.wins / account.closed_trades) * 100)}%`
                : '—'}
            </span>
          </div>
          {!account.enabled && (
            <p className="muted account-note">
              risk disabled — PnL stays at zero
            </p>
          )}
        </div>
      )}
    </section>
  )
}
