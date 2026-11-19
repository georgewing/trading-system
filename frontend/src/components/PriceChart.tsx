import {
  ColorType,
  CrosshairMode,
  createChart,
  type CandlestickData,
  type HistogramData,
  type IChartApi,
  type ISeriesApi,
  type UTCTimestamp,
} from 'lightweight-charts'
import { useEffect, useRef, useState } from 'react'
import type { KlineData } from '../api/envelope'
import { fetchKlineHistory } from '../api/history'
import { useStore } from '../store/createStore'
import { klinesStore, metaStore, replaceKlines } from '../store/terminal'

type RangeKey = '1H' | '6H' | '24H' | 'ALL'

interface RangeOption {
  key: RangeKey
  /** Window length in milliseconds; null means no lower bound. */
  spanMs: number | null
  /** Candles to ask for; the backend clamps at 1000. */
  limit: number
}

const MINUTE_MS = 60_000

const RANGES: RangeOption[] = [
  { key: '1H', spanMs: 60 * MINUTE_MS, limit: 60 },
  { key: '6H', spanMs: 6 * 60 * MINUTE_MS, limit: 360 },
  { key: '24H', spanMs: 24 * 60 * MINUTE_MS, limit: 1000 },
  { key: 'ALL', spanMs: null, limit: 1000 },
]

function toCandle(k: KlineData): CandlestickData {
  return {
    time: (k.open_time / 1000) as UTCTimestamp,
    open: Number(k.open),
    high: Number(k.high),
    low: Number(k.low),
    close: Number(k.close),
  }
}

function toVolume(k: KlineData): HistogramData {
  return {
    time: (k.open_time / 1000) as UTCTimestamp,
    value: Number(k.volume),
    color: Number(k.close) >= Number(k.open) ? '#26a69a55' : '#ef535055',
  }
}

export function PriceChart() {
  const klines = useStore(klinesStore)
  const meta = useStore(metaStore)
  const containerRef = useRef<HTMLDivElement | null>(null)
  const chartRef = useRef<IChartApi | null>(null)
  const candleRef = useRef<ISeriesApi<'Candlestick'> | null>(null)
  const volumeRef = useRef<ISeriesApi<'Histogram'> | null>(null)
  const renderedRef = useRef<{ first: number; length: number }>({
    first: 0,
    length: 0,
  })
  // Raised when a history page replaces the series. A reload of the same
  // window keeps the first open_time, so the diff below cannot tell it apart
  // from a tail update unless the loader says so.
  const replacedRef = useRef(false)
  const aliveRef = useRef(true)
  const [range, setRange] = useState<RangeKey | null>(null)
  const [loading, setLoading] = useState(false)
  const [error, setError] = useState<string | null>(null)

  useEffect(() => {
    aliveRef.current = true
    return () => {
      aliveRef.current = false
    }
  }, [])

  useEffect(() => {
    const el = containerRef.current
    if (!el) return

    const chart = createChart(el, {
      width: el.clientWidth,
      height: el.clientHeight,
      layout: {
        background: { type: ColorType.Solid, color: '#0d1117' },
        textColor: '#8b949e',
        fontSize: 11,
      },
      grid: {
        vertLines: { color: '#161b22' },
        horzLines: { color: '#161b22' },
      },
      rightPriceScale: { borderColor: '#21262d' },
      timeScale: {
        borderColor: '#21262d',
        timeVisible: true,
        secondsVisible: false,
      },
      crosshair: { mode: CrosshairMode.Normal },
    })

    const candles = chart.addCandlestickSeries({
      upColor: '#26a69a',
      downColor: '#ef5350',
      borderUpColor: '#26a69a',
      borderDownColor: '#ef5350',
      wickUpColor: '#26a69a',
      wickDownColor: '#ef5350',
      priceFormat: { type: 'price', precision: 2, minMove: 0.01 },
    })

    const volume = chart.addHistogramSeries({
      priceScaleId: 'volume',
      priceFormat: { type: 'volume' },
      lastValueVisible: false,
      priceLineVisible: false,
    })
    chart.priceScale('volume').applyOptions({
      scaleMargins: { top: 0.82, bottom: 0 },
    })

    chartRef.current = chart
    candleRef.current = candles
    volumeRef.current = volume

    const observer = new ResizeObserver(() => {
      chart.applyOptions({ width: el.clientWidth, height: el.clientHeight })
    })
    observer.observe(el)

    return () => {
      observer.disconnect()
      chart.remove()
      chartRef.current = null
      candleRef.current = null
      volumeRef.current = null
      renderedRef.current = { first: 0, length: 0 }
      replacedRef.current = false
    }
  }, [])

  useEffect(() => {
    const candles = candleRef.current
    const volume = volumeRef.current
    if (!candles || !volume) return

    // Clear explicitly instead of returning early, otherwise the series keeps
    // showing candles the store no longer has.
    if (klines.length === 0) {
      candles.setData([])
      volume.setData([])
      renderedRef.current = { first: 0, length: 0 }
      replacedRef.current = false
      return
    }

    const first = klines[0].open_time
    const last = klines[klines.length - 1]
    const rendered = renderedRef.current

    // A snapshot or a history page replaces the series; a single appended or
    // updated candle does not. Comparing the first open_time alone misses a
    // reload that returns the same window, so the loader raises replacedRef.
    const replaced =
      replacedRef.current ||
      rendered.length === 0 ||
      first !== rendered.first ||
      klines.length < rendered.length

    if (replaced) {
      candles.setData(klines.map(toCandle))
      volume.setData(klines.map(toVolume))
      chartRef.current?.timeScale().fitContent()
      replacedRef.current = false
    } else {
      candles.update(toCandle(last))
      volume.update(toVolume(last))
    }

    renderedRef.current = { first, length: klines.length }
  }, [klines])

  async function loadRange(next: RangeKey): Promise<void> {
    const option = RANGES.find((r) => r.key === next)
    if (!option || loading) return

    setLoading(true)
    setError(null)

    try {
      const rows = await fetchKlineHistory({
        symbol: meta.symbol,
        fromMs: option.spanMs === null ? undefined : Date.now() - option.spanMs,
        limit: option.limit,
      })
      if (!aliveRef.current) return

      // Replace the whole series; later market.kline events merge by open_time.
      replacedRef.current = true
      replaceKlines(rows)
      setRange(next)
    } catch (err) {
      if (!aliveRef.current) return
      // Keep the candles already on screen: only the notice changes.
      setError(err instanceof Error ? err.message : 'history load failed')
    } finally {
      if (aliveRef.current) setLoading(false)
    }
  }

  return (
    <section className="panel panel-chart">
      <div className="panel-head">
        <h2>{meta.symbol} · 1m</h2>
        <div className="chart-controls">
          {error && (
            <span className="chart-error" title={error}>
              {error}
            </span>
          )}
          {loading && <span className="muted">loading…</span>}
          <div className="range-switch" role="group" aria-label="History range">
            {RANGES.map((option) => (
              <button
                key={option.key}
                type="button"
                className={`range-btn${range === option.key ? ' active' : ''}`}
                onClick={() => void loadRange(option.key)}
                disabled={loading}
                aria-pressed={range === option.key}
              >
                {option.key}
              </button>
            ))}
          </div>
          <span className="muted">{klines.length} candles</span>
        </div>
      </div>
      <div className="chart" ref={containerRef} />
    </section>
  )
}
