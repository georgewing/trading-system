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
import { useEffect, useRef } from 'react'
import type { KlineData } from '../api/envelope'
import { useStore } from '../store/createStore'
import { klinesStore, metaStore } from '../store/terminal'

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
    }
  }, [])

  useEffect(() => {
    const candles = candleRef.current
    const volume = volumeRef.current
    if (!candles || !volume || klines.length === 0) return

    const first = klines[0].open_time
    const last = klines[klines.length - 1]
    const rendered = renderedRef.current

    // A snapshot replaces the series; a single appended/updated candle does not.
    if (
      rendered.length === 0 ||
      first !== rendered.first ||
      klines.length < rendered.length
    ) {
      candles.setData(klines.map(toCandle))
      volume.setData(klines.map(toVolume))
      chartRef.current?.timeScale().fitContent()
    } else {
      candles.update(toCandle(last))
      volume.update(toVolume(last))
    }

    renderedRef.current = { first, length: klines.length }
  }, [klines])

  return (
    <section className="panel panel-chart">
      <div className="panel-head">
        <h2>{meta.symbol} · 1m</h2>
        <span className="muted">{klines.length} candles</span>
      </div>
      <div className="chart" ref={containerRef} />
    </section>
  )
}
