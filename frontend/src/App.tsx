import { useEffect } from 'react'
import { fetchMeta } from './api/http'
import { TradingSocket } from './api/ws'
import { Header } from './components/Header'
import { OrderBook } from './components/OrderBook'
import { OrderList } from './components/OrderList'
import { OrderTicket } from './components/OrderTicket'
import { PriceChart } from './components/PriceChart'
import { TradeTape } from './components/TradeTape'
import { metaStore } from './store/terminal'

function topicsFor(symbol: string): string[] {
  return [`depth:${symbol}`, `trade:${symbol}`, `kline:${symbol}:1m`, 'order']
}

export default function App() {
  useEffect(() => {
    let socket: TradingSocket | null = null
    let cancelled = false

    // The symbol comes from the backend config, not from the bundle, so the
    // topic names are always correct for the running gateway.
    fetchMeta()
      .then((meta) => {
        if (cancelled) return
        metaStore.set({
          symbol: meta.symbol,
          pricePrecision: meta.price_precision,
          quantityPrecision: meta.quantity_precision,
          ready: true,
          topics: [],
        })
        socket = new TradingSocket(topicsFor(meta.symbol))
        socket.start()
      })
      .catch(() => {
        if (cancelled) return
        // Keep the UI usable (and visibly disconnected) if /meta is down.
        socket = new TradingSocket(topicsFor('BTCUSDT'))
        socket.start()
      })

    return () => {
      cancelled = true
      socket?.stop()
    }
  }, [])

  return (
    <div className="app">
      <Header />
      <main className="layout">
        <div className="col col-left">
          <OrderBook />
          <TradeTape />
        </div>
        <div className="col col-center">
          <PriceChart />
        </div>
        <div className="col col-right">
          <OrderTicket />
          <OrderList />
        </div>
      </main>
    </div>
  )
}
