# AGENTS.md

## 1. Purpose

本项目目标是构建低延迟量化交易系统的核心骨架，当前重点在 Go 后端撮合链路与基础可观测性。  
Agent 在本仓库中的职责是：在不破坏主链路稳定性的前提下，持续推进“可运行、可观测、可部署”的工程化能力。

## 2. Current Scope

当前已实现（以仓库现状为准）：
- 单体入口：`cmd/hft-server/main.go`
- 配置加载：YAML 严格解析（`internal/app/config.go`）
- 撮合主链路：HTTP 下单 -> EventBus -> Matcher -> OutboundWorker
- 出向处理：WS Sink + Storage Sink（当前 storage 为内存实现）
- 基础指标：Prometheus（订单到达、撮合耗时、队列积压）
- 基础部署：`deployments/docker-compose.yml`（Postgres/ClickHouse/Redis + Postgres migrate service）
- SQL：`migrations/postgres/*` 与 `internal/storage/clickhouse/*`

当前未完成（禁止假设已具备）：
- 交易所市场网关（行情订阅/解码/重连）
- 真实数据库写入层（PG/CH writer）
- 策略回测框架
- 完整风控链路（事前/事中）
- React+TS 前端终端
- CI、系统化测试与基准压测

## 3. Repository Map

- `cmd/hft-server`：进程入口，负责配置加载和生命周期控制
- `internal/app`：依赖组装、服务启动、shutdown
- `internal/engine`：订单簿与撮合逻辑
- `internal/pipeline`：事件总线与出向 worker
- `internal/transport/http`：下单 API
- `internal/transport/ws`：执行回报订阅 hub
- `internal/observability`：指标接口与 Prometheus 实现
- `pkg/ringbuffer`：SPSC ringbuffer
- `pkg/idgen`：Snowflake ID 生成
- `migrations/postgres`：Postgres 版本化迁移
- `internal/storage/clickhouse`：ClickHouse DDL
- `deployments`：本地 compose 与 k8s 草案

## 4. Runtime Contracts

- API:
  - `POST /api/v1/order`
- Metrics:
  - `GET /metrics`（地址来自配置）
- 必填环境变量：
  - `SNOWFLAKE_WORKER_ID`
- 必填配置来源：
  - `-config` 或 `APP_CONFIG`（YAML）

## 5. Local Development Commands

- 全量编译测试：
  - `GOCACHE=/tmp/go-build go test ./...`
- 启动服务：
  - `SNOWFLAKE_WORKER_ID=1 GOCACHE=/tmp/go-build go run ./cmd/hft-server -config ./configs/config.example.yaml`
- 基础设施：
  - `docker compose -f deployments/docker-compose.yml up -d postgres clickhouse redis`
  - `docker compose -f deployments/docker-compose.yml run --rm migrate-postgres`

## 6. Agent Behavior

- 默认情况下，Agent 不得直接修改源码、配置、脚本、测试或文档文件。Agent 应先阅读代码、分析实现状态、识别风险、设计方案，并在回复中提供具体代码片段或 patch 建议。
- 如果用户意图不明确，必须按“禁止修改文件”处理，只提供分析、计划或代码示例。
- 除非用户明确要求，不要运行会改变仓库状态的命令。用于分析的只读命令可以使用，例如 `rg`、`sed`、`cat`、`git status`、`go test`；但不要擅自启动服务、运行格式化器、代码生成器、数据库迁移或依赖安装。

## 7. Engineering Rules For Agents

- 只做最小必要改动，优先修复正确性与稳定性，再做扩展。
- 不得绕过配置系统硬编码关键运行参数。
- 不得把 `.down.sql` 用于初始化路径。
- 涉及存储结构变更时，必须同步迁移脚本与文档。
- 任何“接收请求但未处理”的路径必须有明确错误语义和指标。
- 涉及撮合主链路改动时，必须评估延迟、背压、丢消息风险。
- 未经明确要求，不引入重量级依赖或复杂框架。
- 保持 ASCII 文本与清晰注释，避免无意义注释。

## 8. Data & Migration Policy

- Postgres 仅使用 `migrations/postgres/*.up.sql` 做正向迁移。
- ClickHouse DDL 以 `internal/storage/clickhouse` 为源，`deployments/init/clickhouse` 用于容器初始化。
- 任何表结构变更必须满足：
  - 可回滚（至少提供 down 或替代回滚方案）
  - 不破坏已有读写路径
  - 在 README/部署说明中更新执行方式

## 9. Observability Baseline

以下指标必须持续可用：
- `order_arrival_total{result=accepted|rejected|canceled}`
- `match_latency_seconds{result=ok|error}`
- `queue_backlog{queue=inbound|outbound}`

新增链路时，至少补齐：
- 吞吐计数
- 延迟分布
- 背压/丢弃计数

## 10. Definition Of Done (DoD)

一个任务可合并需满足：
- `go test ./...` 通过
- 启动链路可跑通（配置加载 + API + metrics）
- 关键错误路径有明确返回与日志
- 指标不倒退（至少不移除现有核心指标）
- 变更范围与目标一致，无顺手重构噪音

## 11. Near-term Priorities

P0:
- 修复启动失败不向上返回导致“假存活”的问题（API/metrics listen error 应触发进程失败）

P1:
- 落地真实 Postgres/ClickHouse writer，替换内存 repo
- 增加撮合与 pipeline 的单测/压测基线

P2:
- 完成基础风控拦截（下单前校验、限频、限仓位）
- 增加最小可用回测数据读取接口
