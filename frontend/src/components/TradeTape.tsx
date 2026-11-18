import { useLayoutEffect, useRef, useState, type UIEvent } from 'react'
import { formatClock, formatPrice, formatQty } from '../lib/format'
import { useStore } from '../store/createStore'
import { tradesStore } from '../store/terminal'

/**
 * Fixed row height in px. Must stay in sync with `.tape-row` in global.css:
 * 16px line-height plus 2px padding on each side.
 */
const ROW_HEIGHT = 20

/** Off-screen rows rendered above and below the viewport to mask scroll jitter. */
const OVERSCAN = 10

interface TapeViewport {
  scrollTop: number
  height: number
}

export function TradeTape() {
  const trades = useStore(tradesStore)
  const bodyRef = useRef<HTMLDivElement | null>(null)
  const [viewport, setViewport] = useState<TapeViewport>({
    scrollTop: 0,
    height: 0,
  })

  // Measure before paint so the first frame already shows the real window, and
  // follow later layout changes (window resize, panel resize) via ResizeObserver.
  useLayoutEffect(() => {
    const el = bodyRef.current
    if (!el) return

    const measure = () => {
      setViewport((prev) =>
        prev.scrollTop === el.scrollTop && prev.height === el.clientHeight
          ? prev
          : { scrollTop: el.scrollTop, height: el.clientHeight },
      )
    }

    measure()

    const observer = new ResizeObserver(measure)
    observer.observe(el)
    return () => observer.disconnect()
  }, [])

  // The handler only reads scrollTop. Nothing in this component writes it, so a
  // data refresh cannot yank a user who is parked on older prints back to the top.
  const handleScroll = (event: UIEvent<HTMLDivElement>) => {
    const scrollTop = event.currentTarget.scrollTop
    setViewport((prev) =>
      prev.scrollTop === scrollTop ? prev : { ...prev, scrollTop },
    )
  }

  const total = trades.length
  const contentHeight = total * ROW_HEIGHT
  const maxScrollTop = Math.max(0, contentHeight - viewport.height)
  const offset = Math.min(Math.max(viewport.scrollTop, 0), maxScrollTop)

  const firstVisible = Math.floor(offset / ROW_HEIGHT)
  const start = Math.max(0, Math.min(total, firstVisible - OVERSCAN))
  const end = Math.min(
    total,
    Math.max(
      start,
      Math.ceil((offset + viewport.height) / ROW_HEIGHT) + OVERSCAN,
    ),
  )
  const visible = trades.slice(start, end)
  const padTop = start * ROW_HEIGHT
  const padBottom = (total - end) * ROW_HEIGHT

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

        <div className="tape-body" ref={bodyRef} onScroll={handleScroll}>
          {total === 0 && <div className="empty">no prints yet…</div>}

          {total > 0 && (
            <>
              <div className="tape-spacer" style={{ height: padTop }} />

              {visible.map((trade) => (
                <div className="tape-row" key={trade.id}>
                  <span
                    className={`px ${trade.side === 'BUY' ? 'bid' : 'ask'}`}
                  >
                    {formatPrice(trade.price)}
                  </span>
                  <span className="qty">{formatQty(trade.qty, 5)}</span>
                  <span className="time">{formatClock(trade.ts)}</span>
                </div>
              ))}

              <div className="tape-spacer" style={{ height: padBottom }} />
            </>
          )}
        </div>
      </div>
    </section>
  )
}
