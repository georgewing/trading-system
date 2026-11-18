# 行情链路收口交接（internal/hft 布局）

状态时间点：commit `b036897`（D1 契约 + Binance 解码/接入骨架）之后。
本文件由上一轮执行者留下，供继续收口的人接手，**不含代码改动**。

## 1. 目标布局

```
internal/hft/
├── domain/          # 契约层（冻结）
├── infra/
│   ├── binance/     # 交易所接入（解码 + dialer + source）
│   ├── memstore/    # 内存快照 + 扇出（读侧）
│   └── orders/      # 订单读模型（待迁入）
└── api/             # HTTP / WS 交付层（待迁入）
```

## 2. 已完成

- `internal/hft/domain`：`Event`/`Trade`/`Tick`（int64 最小单位）、`Source`/`Sink` 端口、
  `MemFanout`（topic 扇出，带慢消费者丢弃计数）。
- `internal/hft/infra/binance/decode.go`：纯函数解码，trade + BBO，十进制字符串不四舍五入。
- `internal/hft/infra/binance/source.go`：可注入 dialer 的重连骨架（退避 + 抖动 + 三类计数器）。
- **`internal/gateway/binance/{client.go,client_test.go}` 已移入 `internal/hft/infra/binance/`**，
  `internal/gateway/` 已删除；`internal/app/app.go`、`scripts/dev/feedcheck/main.go` 的 import 已同步。

## 3. 待收口（按依赖顺序）

1. 修构建红灯（见第 5 节）。
2. `internal/market`（`Store` 快照/扇出 + string 类型的 `Trade`/`Depth`/`Kline`）
   → 合并进 `internal/hft/infra/memstore`，并把 `internal/market` 的 width 模型并入 `domain`
   或明确保留为 wire 模型（两份模型必须写清用途，否则会再次分叉）。
3. `internal/orders`（订单读模型）→ `internal/hft/infra/orders`。
4. `internal/transport/http/market.go`、`internal/transport/ws/server.go` → `internal/hft/api/`。
   注意：`market.go` 依赖同包的 `writeError`，迁移时需自带一个本地副本。
5. 删除重复实现：
   - `internal/hft/domain/fanout.go` + `Fanout` 端口 —— 与 `memstore` 的广播重复；
     存储侧消费改用 `Store.Subscribe`。
   - `internal/hft/infra/binance/client.go` —— 与 `Source`+`Decoder` 的新链路重复；
     迁移前先确认 `depth`/`kline` 已由新链路覆盖（前端依赖这两个 topic）。
6. 保持对外契约不变：`/api/v1/market/{depth,trades,kline}`、`/api/v1/orders[/{id}]`、
   `/api/v1/meta`、`/ws` 的路径与 JSON 字段（`order_id` 为字符串）。

## 4. 两份模型的边界（不要合并）

- **domain.Event（int64）**：存储/撮合侧契约，最小单位，供 ClickHouse writer 使用。
- **wire 模型（string）**：前端交付模型，保留交易所原始十进制字符串，避免 JS 精度问题。
  两者在存储写入边界做一次转换（`engine.ParseQuantity`，拒绝而非四舍五入）。

## 5. 已知构建红灯（均非本次搬迁引入）

```
internal/hft/infra/binance/dialer.go:143:2: declared and not used: ctx
internal/storage/postgres/writer.go:17:2: no required module provides package github.com/jackc/pgx/v5/stdlib
```

- 前者：`wsStream.Close()` 里的 `ctx, cancel := context.WithTimeout(...)` 未使用 `ctx`，删掉即可。
- 后者：`go get github.com/jackc/pgx/v5` 后 `go mod tidy`。

验证基线：`go test -race ./internal/hft/domain` 通过。

## 6. 未提交

本次搬迁与 import 修改留在工作区，未提交（同一时间窗内 `internal/hft/**`、`app.go` 还有并行改动）。
