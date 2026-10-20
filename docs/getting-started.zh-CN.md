# 快速上手

[English](getting-started.md) · [总体架构](architecture.zh-CN.md)

## 1. 环境要求

| 工具 | 版本 | 用途 |
|---|---|---|
| Go | 1.27+ | 全部 |
| Docker + Compose | 较新版本 | MongoDB 模式与分布式模式（可选） |
| buf + protoc-gen-go | 见 `proto/README.zh-CN.md` | 仅在修改 `.proto` 文件时需要 |

## 2. 单体模式运行（零依赖）

```bash
go run ./cmd/server -config configs/allinone.yaml
```

```
level=INFO msg="starting node" id=dev-1 roles="[gate login game]" bus=local registry=memory storage=memory
level=INFO msg="gate listening" module=gate addr=[::]:7000 transport=*network.TCPAcceptor
level=INFO msg="gate listening" module=gate addr=[::]:7001 transport=*network.WSAcceptor
level=INFO msg="server started" module=app
```

端口：`7000` TCP 客户端、`7001` WebSocket（`/ws`）、`127.0.0.1:9100` 管理端口。

## 3. 在浏览器中测试

打开 **http://127.0.0.1:9100/**（会自动跳转到 `/console/`）。

* 点击**运行自检**：在独立连接上执行 13 项检查（连接、心跳、未登录拦截、登录、进入游戏、背包、
  使用道具、错误处理、聊天、移动、防瞬移、GM、断开）；出现绿色的"全部通过"就说明服务器整条链路正常。
* 也可以手动操作：连接 → 登录 → 进入游戏，然后使用道具、聊天，用 WASD / 方向键或点击地图移动。
  每一个数据包都会显示在消息日志里。
* 再开一个窗口，就能看到两个玩家互动。

详见 [game/webconsole](../game/webconsole/README.zh-CN.md)。

## 4. 用机器人试玩

另开一个终端：

```bash
go run ./cmd/robot -n 20 -duration 10s                       # TCP
go run ./cmd/robot -ws ws://127.0.0.1:7001/ws -n 5 -prefix ws  # WebSocket
```

```
==== robot report (4.152s) ====
robots:       20
login ok:     20
login failed: 0
requests:     251 (errors 0)
avg latency:  4.5ms
pushes recv:  3408
kicked:       0
```

每个机器人会登录、进入游戏、查看背包、使用道具、在世界频道打招呼，然后随机走动
（`pushes recv` 中就包含 AOI 推送）。

## 5. 查看运行状态

```bash
curl http://127.0.0.1:9100/readyz
curl -s http://127.0.0.1:9100/metrics | grep ^gs_
go tool pprof http://127.0.0.1:9100/debug/pprof/heap
```

## 6. 持久化存储（MongoDB）

```bash
docker compose -f deploy/docker-compose.yml up -d mongo
go run ./cmd/server -config configs/allinone-mongo.yaml
```

数据写入 `game` 库的 `accounts` 和 `players` 集合。

## 7. 分布式集群

```bash
docker compose -f deploy/docker-compose.yml up -d --build
docker compose -f deploy/docker-compose.yml ps
go run ./cmd/robot -n 50 -duration 20s
docker compose -f deploy/docker-compose.yml logs -f game-1
```

拓扑：`nats`、`redis`、`mongo`、`gate-1`（端口 7000/7001/9100）、`login-1`、`game-1`、`game-2`。
玩家按 uid 分布在 `game-1`/`game-2` 上；世界聊天跨节点。

扩容：复制一个 `game-N` 块并设置新的 `NODE_ID`，或手动多启动几个进程：

```bash
NODE_ID=game-3 NATS_URL=nats://127.0.0.1:4222 REDIS_ADDR=127.0.0.1:6379 \
MONGO_URI=mongodb://127.0.0.1:27017 ADMIN_ADDR=127.0.0.1:9103 \
go run ./cmd/server -config configs/game.yaml
```

停止：`docker compose -f deploy/docker-compose.yml down`（加 `-v` 会清空数据）。

### 7b. Kubernetes（Operator）

需要 Docker、[kind](https://kind.sigs.k8s.io/)、kubectl 和 Helm。

```bash
kind create cluster --name gs
docker build -t gameserver:dev -f deploy/Dockerfile .
docker build -t gameserver-operator:dev operator/
kind load docker-image gameserver:dev gameserver-operator:dev --name gs
helm install gs deploy/helm/gameserver -n game --create-namespace
kubectl get gamecluster -n game -w        # 等到 PHASE 变为 Running
kubectl port-forward -n game svc/demo-gate 7000:7000 7001:7001
go run ./cmd/robot -addr 127.0.0.1:7000 -n 200 -duration 5m -reconnect   # 观察自动扩容
```

详见 [Operator](../operator/README.zh-CN.md)、[Helm Chart](../deploy/helm/gameserver/README.zh-CN.md)。

## 8. 测试

```bash
go test ./...              # 单元 + 端到端 + 分布式（内嵌 NATS、miniredis）
go test -race ./...        # 需要 C 编译器（cgo）
go test -run TestFullGameFlow -v ./test/e2e/
```

| 测试集 | 证明了什么 |
|---|---|
| `framework/...` | 网络分帧、actor 语义、总线/注册中心/哈希、路由器、ID、配置、配置表 |
| `game/logic` | AOI、背包、等级规则 |
| `test/e2e` | 通过真实 TCP 与 WebSocket 测试完整服务器：登录、AOI、背包、聊天、移动、GM、顶号、断线与停服存盘、50 个并发玩家 |
| `test/cluster` | 2 网关 + 1 登录 + 2 游戏节点，走真实 NATS 与 Redis 协议：玩家分布、跨节点聊天、跨网关重登、存盘；扩容、排空、停服与崩溃接管 |
| `operator/...` | 伸缩/排空/滚动升级规则，以及针对假 API Server 的完整调和流程（`cd operator && go test ./...`） |
| `test/k8s` | Operator 在真实 Kubernetes 集群上运行，160 个机器人全程在线（`bash test/k8s/e2e.sh`） |

## 9. 配置参考

| 段 | 键 | 默认值 | 说明 |
|---|---|---|---|
| `node` | `id` | — | 唯一节点 ID（`[A-Za-z0-9_-]`） |
| | `roles` | — | `gate`、`login`、`game` 的子集 |
| | `worker_id` | 0 | 雪花 worker（每个登录节点唯一） |
| `log` | `level` / `format` / `add_source` | info / text / false | |
| `cluster` | `bus` | local | `local` 或 `nats` |
| | `nats_url` | — | |
| | `registry` / `registry_ttl` | memory / 10s | `memory` 或 `redis` |
| `storage` | `driver` | memory | `memory` 或 `mongo` |
| `redis` | `addr` `password` `db` `pool_size` | 127.0.0.1:6379 | |
| `mongo` | `uri` `database` `pool_size` | mongodb://127.0.0.1:27017, game | |
| `admin` | `addr` | —（关闭） | |
| `console` | `enabled` | false | 在管理端口提供 `/console/` 网页控制台（仅开发使用） |
| `gate` | `tcp_addr` `ws_addr` `ws_path` | —、—、/ws | |
| | `max_conns` `max_packet_size` `send_queue_size` | 0、64KiB、256 | |
| | `idle_timeout` `rate_limit` `rate_burst` | 60s、50、100 | |
| `login` | `bcrypt_cost` `max_concurrent` `request_timeout` | 10、256、10s | |
| `game` | `save_interval` `gm_enabled` `chat_interval` `max_move_step` | 30s、false、1s、50 | |

所有值都可以引用环境变量：`${NAME:-默认值}`。

## 10. 常见问题

| 现象 | 原因 / 解决 |
|---|---|
| `cluster.bus=local requires roles gate, login and game` | 本地总线只能在单进程内使用；用 `allinone.yaml` 或改用 NATS |
| `listen tcp :7000: bind: address already in use` | 已有服务器在运行；修改 `gate.tcp_addr` |
| compose 报 `ports are not available: … 7000` | 端口被其他程序占用；设置 `GATE_TCP_PORT=17000 GATE_WS_PORT=17001 GATE_ADMIN_PORT=19100` 后启动，机器人使用 `-addr 127.0.0.1:17000` |
| 登录返回 `SERVICE_UNAVAILABLE` | 没有注册的游戏节点（分布式：启动游戏节点，检查 Redis） |
| `redis ping … connection refused` | 启动 Redis，或使用 `registry: memory` |
| `config: decode: field xyz not found` | YAML 键名拼写错误（严格模式） |
