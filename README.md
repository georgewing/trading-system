目录结构
```
trading-system/
├── cmd/
│   ├── hft-server/              # 单体入口：gateway + api + ws（后期可拆 cmd）
│   │   └── main.go
│   └── tools/                   # 可选：migrate、replay、loadtest
│       └── ...
├── api/
│   └── openapi/                 # 可选：OpenAPI 契约
├── configs/
│   ├── config.example.yaml
│   └── ...
├── deployments/
│   ├── docker-compose.yaml      # 可选：postgres+timescale、clickhouse、redis
│   └── ...
├── internal/
│   ├── app/                     # 组装：依赖注入、生命周期、配置加载
│   │   ├── app.go
│   │   └── config.go
│   ├── engine/                  # 领域模型 + 内存撮合/订单簿（若有）
│   │   ├── types.go
│   │   ├── orderbook.go         # 可选
│   │   └── matcher.go           # 可选
│   ├── gateway/                 # 交易所适配：行情 WS、交易 REST/WS
│   │   ├── market/              # 解码、订阅、重连
│   │   └── execution/           # 下单、撤单、查询
│   ├── pipeline/                # RingBuffer / 事件总线 / worker 池
│   │   └── bus.go
│   ├── risk/                    # 事前/事中规则
│   │   └── guard.go
│   ├── strategy/                # 策略接口与示例（回测与实盘共用接口）
│   │   ├── runner.go
│   │   └── noop.go
│   ├── transport/               # 对前端：HTTP + WebSocket
│   │   ├── http/
│   │   └── ws/
│   ├── storage/                 # 持久化：SQL、CH writer
│   │   ├── postgres/
│   │   └── clickhouse/
│   └── observability/           # metrics、tracing、logging 封装
│       └── ...
├── pkg/
│   ├── ringbuffer/              # 已有
│   ├── protocol/                # 可选：与前端/策略的二进制或 JSON 契约
│   └── idgen/
├── migrations/                  # SQL 迁移（推荐 golang-migrate 或 goose）
│   └── postgres/
│       ├── 000001_init.up.sql
│       └── 000001_init.down.sql
├── scripts/
├── go.mod
└── README.md
```
