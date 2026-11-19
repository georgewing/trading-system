# 架构与依赖规则

本文件定义 `internal/hft` 布局下的分层与依赖方向，是判断"改得干不干净"的唯一标准。
新增/搬迁包时按第 2 节规则校验，命令见第 6 节。

## 1. 分层与目录

| 层 | 目录 | 职责 | 允许依赖 |
|---|---|---|---|
| L0 领域 | `internal/hft/domain` | 行情/订单的实体与端口（`Event`/`Trade`/`Tick`/`Depth`/`Kline`、`Source`/`Sink`/`Fanout`/`Snapshotter`） | 仅标准库 |
| L0 领域 | `internal/hft/engine` | 订单簿、撮合、订单/成交类型 | `internal/infrastructure/datastructures`、`pkg/*` |
| L0 领域 | `internal/hft/risk`、`internal/strategy` | 风控规则、策略接口 | `internal/hft/engine` |
| L1 应用 | `internal/pipeline` | 订单命令编排：风控 → 撮合 → 执行回报 | 领域层 + 端口 |
| L2 适配器 | `internal/gateway/binance/market` | 行情接入（解码 + dialer + 重连） | `hft/domain`、`hft/engine` |
| L2 适配器 | `internal/hft/infra/memstore` | 行情快照（读侧） | `hft/domain` |
| L2 适配器 | `internal/hft/infra/orders` | 订单读模型 | `hft/engine` |
| L2 适配器 | `internal/storage/{clickhouse,postgres}` | 持久化 writer | 标准库/驱动 |
| L2 适配器 | `internal/observability` | 指标采集实现（Prometheus） | 端口定义 |
| L2 适配器 | `internal/execution`、`internal/routing` | venue 抽象与路由 | `hft/engine` |
| L3 交付 | `internal/transport/{http,ws,wire}` | HTTP/WS 适配、wire 编码 | 应用层 + 领域层 |
| L4 组装 | `internal/app` | 组合根：读配置、构造适配器、生命周期 | 所有层 |

## 2. 依赖规则（违反即为脏）

1. **`internal/hft/domain` 零内部依赖**。任何 `trading-system/internal/...` 的 import 都不允许。
2. **依赖只能向内**：L2/L3 不得被 L0/L1 依赖；`pipeline` 不得 import `transport`、`app`、`infra`。
3. **端口在领域层，实现的选取在组合根**：`pipeline` 只依赖接口（`RiskGuard`、`Matcher`、`Recorder`），具体类型由 `app` 注入。
4. **交付层不直接访问持久化/网络适配器**：`transport/*` 只能读端口与读模型，不能 import `storage/*`、`gateway/binance/market`。
5. **同一职责只有一份实现**：新增实现前先查第 4 节的重复清单。

## 3. 当前依赖图（关键边）

```
cmd/hft-server      → app
cmd/backtest        → backtest, strategy
app                 → 所有层（组合根，允许）
transport/http      → domain, engine, infra/orders, pipeline, wire
transport/ws        → domain, engine, infra/orders, pipeline, wire, observability
pipeline            → engine, execution, risk, observability
gateway/binance/market → domain, engine
infra/memstore      → domain
infra/orders        → engine
domain              → （无）
```

## 4. 已知待收敛项

| 项 | 现状 | 目标 | 说明 |
|---|---|---|---|
| `internal/transport/*` | L3 在顶层 | `internal/hft/api`（空占位已建） | 迁移时注意 `http`/`ws` 两个包的符号重名，需一次性合并或分层保留 |
| `internal/app` | 组合根在顶层 | `internal/hft/bootstrap`（空占位已建） | 纯搬迁 |
| `internal/storage/*` | L2 在顶层 | `internal/hft/infra/*` | 纯搬迁 |
| `internal/infrastructure/datastructures` | 被 `hft/engine` 依赖 | `internal/hft/infra/datastructures` 或 `hft/engine/datastructures` | 纯搬迁 |
| `internal/observability` | 端口与 Prometheus 实现同包 | 端口留领域层，实现进 `hft/infra` | **需拆包**，不是搬迁 |
| `internal/execution` | 端口（`Venue`/`Matcher`）与 `PaperVenue` 实现同包 | 端口进应用/领域层，实现进 `hft/infra` | **需拆包** |
| `internal/pipeline` | 依赖具体 `risk`/`execution` 类型 | 收敛到端口 | 逐步替换，勿一次性改 |

## 5. 两条不能踩的边界

- **两份模型不要合并**：`domain.Event`（int64 最小单位，撮合/存储用）与 wire 层（string，前端交付用，保留交易所原始十进制）。
  转换点在存储写入边界，使用 `ParseQuantity`（超精度拒绝，不四舍五入）。
- **`domain.MemFanout` 不是 `memstore` 的重复**：`memstore` 提供 REST/首订阅快照，`MemFanout` 提供实时增量推送。
  删除 `MemFanout` 会直接打断 `/ws`。

## 6. 校验

```sh
GOCACHE=/tmp/go-build go build ./...
GOCACHE=/tmp/go-build go test -race ./internal/hft/...

# 依赖方向检查：domain 必须没有任何 internal 依赖
GOCACHE=/tmp/go-build go list -f '{{.ImportPath}} -> {{join .Imports " "}}' ./... \
  | grep 'hft/domain ' | grep -c 'trading-system/internal'   # 期望 0
```

每次搬迁只做一个包，搬迁后立即跑上面两条命令，保持回滚粒度可控。
