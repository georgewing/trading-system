import { useEffect, useState } from 'react'
import { cancelOrder, fetchOrders } from '../api/http'
import type { OrderStatus } from '../api/envelope'
import { formatClock, formatQty, scaledToDecimal, ticksToPrice } from '../lib/format'
import { useStore } from '../store/createStore'
import { mergeOrders, metaStore, ordersStore } from '../store/terminal'

const STATUS_CLASS: Record<OrderStatus, string> = {
  NEW: 'st-new',
  PARTIALLY_FILLED: 'st-partial',
  FILLED: 'st-filled',
  CANCELED: 'st-canceled',
  REJECTED: 'st-rejected',
  UNKNOWN: 'st-unknown',
}

export function OrderList() {
  const orders = useStore(ordersStore)
  const meta = useStore(metaStore)
  const [error, setError] = useState<string | null>(null)
  const [canceling, setCanceling] = useState<string | null>(null)

  useEffect(() => {
    let cancelled = false

    fetchOrders()
      .then((rows) => {
        if (!cancelled) mergeOrders(rows)
      })
      .catch((err: unknown) => {
        if (!cancelled) {
          setError(err instanceof Error ? err.message : 'load failed')
        }
      })

    return () => {
      cancelled = true
    }
  }, [])

  async function handleCancel(orderId: string) {
    setCanceling(orderId)
    setError(null)
    try {
      await cancelOrder(orderId)
    } catch (err) {
      setError(err instanceof Error ? err.message : 'cancel failed')
    } finally {
      setCanceling(null)
    }
  }

  return (
    <section className="panel panel-orders">
      <div className="panel-head">
        <h2>Orders</h2>
        <span className="muted">{orders.length}</span>
      </div>

      {error && <div className="empty err">{error}</div>}

      <div className="orders">
        <div className="orders-labels">
          <span>Time</span>
          <span>Side</span>
          <span>Price</span>
          <span>Qty</span>
          <span>Filled</span>
          <span>Status</span>
          <span />
        </div>

        <div className="orders-body">
          {orders.length === 0 && !error && (
            <div className="empty">no orders yet</div>
          )}

          {orders.map((order) => (
            <div className="orders-row" key={order.order_id}>
              <span className="time">{formatClock(order.created_at)}</span>
              <span className={order.side === 'BUY' ? 'px bid' : 'px ask'}>
                {order.side}
              </span>
              <span className="qty">
                {order.price > 0
                  ? ticksToPrice(order.price, meta.pricePrecision)
                  : '—'}
              </span>
              <span className="qty">
                {order.quantity ? formatQty(order.quantity, 5) : '—'}
              </span>
            			  <span className="qty">{scaledToDecimal(order.filled, meta.quantityPrecision)}</span>
              <span className={`status ${STATUS_CLASS[order.status]}`}>
                {order.status}
              </span>
              <span className="row-action">
                {(order.status === 'NEW' ||
                  order.status === 'PARTIALLY_FILLED') && (
                  <button
                    type="button"
                    className="cancel-btn"
                    disabled={canceling === order.order_id}
                    onClick={() => handleCancel(order.order_id)}
                  >
                    {canceling === order.order_id ? '…' : 'Cancel'}
                  </button>
                )}
              </span>
            </div>
          ))}
        </div>
      </div>

      {orders.some((o) => o.reason) && (
        <p className="muted orders-reason">
          last reason: {orders.find((o) => o.reason)?.reason}
        </p>
      )}
    </section>
  )
}
