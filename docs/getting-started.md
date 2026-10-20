# Getting started

[中文](getting-started.zh-CN.md) · [Architecture](architecture.md)

## 1. Prerequisites

| Tool | Version | Needed for |
|---|---|---|
| Go | 1.27+ | everything |
| Docker + Compose | recent | MongoDB mode and distributed mode (optional) |
| buf + protoc-gen-go | see `proto/README.md` | only when changing `.proto` files |

## 2. Run all-in-one (zero dependencies)

```bash
go run ./cmd/server -config configs/allinone.yaml
```

```
level=INFO msg="starting node" id=dev-1 roles="[gate login game]" bus=local registry=memory storage=memory
level=INFO msg="gate listening" module=gate addr=[::]:7000 transport=*network.TCPAcceptor
level=INFO msg="gate listening" module=gate addr=[::]:7001 transport=*network.WSAcceptor
level=INFO msg="server started" module=app
```

Ports: `7000` TCP clients, `7001` WebSocket (`/ws`), `127.0.0.1:9100` admin.

## 3. Test in the browser

Open **http://127.0.0.1:9100/** (redirects to `/console/`).

* Click **Run self-test**: 13 checks (connect, heartbeat, auth guard, login,
  enter game, bag, item use, error handling, chat, movement, anti-teleport, GM,
  disconnect) run on a separate connection; a green "All passed" means the
  server works end to end.
* Or play manually: Connect → Log in → Enter game, then use items, chat, and
  move with WASD / arrow keys or by clicking the map. Every packet appears in
  the message log.
* Open a second window to see two players interact.

Details: [game/webconsole](../game/webconsole/README.md).

## 4. Play with robots

In another terminal:

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

Each robot logs in, enters the game, reads its bag, uses an item, says hello in
world chat and random-walks (you can see the AOI pushes in `pushes recv`).

## 5. Inspect

```bash
curl http://127.0.0.1:9100/readyz
curl -s http://127.0.0.1:9100/metrics | grep ^gs_
go tool pprof http://127.0.0.1:9100/debug/pprof/heap
```

## 6. Persistent storage (MongoDB)

```bash
docker compose -f deploy/docker-compose.yml up -d mongo
go run ./cmd/server -config configs/allinone-mongo.yaml
```

Data lands in database `game`, collections `accounts` and `players`.

## 7. Distributed cluster

```bash
docker compose -f deploy/docker-compose.yml up -d --build
docker compose -f deploy/docker-compose.yml ps
go run ./cmd/robot -n 50 -duration 20s
docker compose -f deploy/docker-compose.yml logs -f game-1
```

Topology: `nats`, `redis`, `mongo`, `gate-1` (ports 7000/7001/9100),
`login-1`, `game-1`, `game-2`. Players are spread across `game-1`/`game-2`
by uid; world chat crosses nodes.

Scale out: copy a `game-N` block with a new `NODE_ID`, or run more processes
by hand:

```bash
NODE_ID=game-3 NATS_URL=nats://127.0.0.1:4222 REDIS_ADDR=127.0.0.1:6379 \
MONGO_URI=mongodb://127.0.0.1:27017 ADMIN_ADDR=127.0.0.1:9103 \
go run ./cmd/server -config configs/game.yaml
```

Stop: `docker compose -f deploy/docker-compose.yml down` (add `-v` to wipe data).

## 8. Tests

```bash
go test ./...              # unit + e2e + distributed (embedded NATS, miniredis)
go test -race ./...        # needs a C compiler (cgo)
go test -run TestFullGameFlow -v ./test/e2e/
```

| Suite | What it proves |
|---|---|
| `framework/...` | network framing, actor semantics, bus/registry/hashing, router, ids, config, tables |
| `game/logic` | AOI, bag, level rules |
| `test/e2e` | a full server over real TCP and WebSocket: login, AOI, bag, chat, movement, GM, duplicate login, save on disconnect and shutdown, 50 concurrent players |
| `test/cluster` | 2 gates + 1 login + 2 game nodes over real NATS + Redis protocol: spreading, cross-node chat, cross-gate re-login, save |

## 9. Configuration reference

| Section | Key | Default | Description |
|---|---|---|---|
| `node` | `id` | — | unique node id (`[A-Za-z0-9_-]`) |
| | `roles` | — | subset of `gate`, `login`, `game` |
| | `worker_id` | 0 | Snowflake worker (unique per login node) |
| `log` | `level` / `format` / `add_source` | info / text / false | |
| `cluster` | `bus` | local | `local` or `nats` |
| | `nats_url` | — | |
| | `registry` / `registry_ttl` | memory / 10s | `memory` or `redis` |
| `storage` | `driver` | memory | `memory` or `mongo` |
| `redis` | `addr` `password` `db` `pool_size` | 127.0.0.1:6379 | |
| `mongo` | `uri` `database` `pool_size` | mongodb://127.0.0.1:27017, game | |
| `admin` | `addr` | — (disabled) | |
| `console` | `enabled` | false | serve the web console at `/console/` on the admin port (development only) |
| `gate` | `tcp_addr` `ws_addr` `ws_path` | — , — , /ws | |
| | `max_conns` `max_packet_size` `send_queue_size` | 0, 64KiB, 256 | |
| | `idle_timeout` `rate_limit` `rate_burst` | 60s, 50, 100 | |
| `login` | `bcrypt_cost` `max_concurrent` `request_timeout` | 10, 256, 10s | |
| `game` | `save_interval` `gm_enabled` `chat_interval` `max_move_step` | 30s, false, 1s, 50 | |

Every value can reference environment variables: `${NAME:-default}`.

## 10. Troubleshooting

| Symptom | Cause / fix |
|---|---|
| `cluster.bus=local requires roles gate, login and game` | local bus only works in one process; use `allinone.yaml` or switch to NATS |
| `listen tcp :7000: bind: address already in use` | another server running; change `gate.tcp_addr` |
| compose: `ports are not available: … 7000` | another program holds the port; run with `GATE_TCP_PORT=17000 GATE_WS_PORT=17001 GATE_ADMIN_PORT=19100` and point robots at `-addr 127.0.0.1:17000` |
| login returns `SERVICE_UNAVAILABLE` | no game node registered (distributed: start a game node, check Redis) |
| `redis ping … connection refused` | start Redis or use `registry: memory` |
| `config: decode: field xyz not found` | typo in YAML key (strict mode) |
