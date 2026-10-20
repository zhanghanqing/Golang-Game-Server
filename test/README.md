# test — integration tests

[中文](README.zh-CN.md)

Unit tests live next to their packages. This directory holds tests that start
**whole servers** and talk to them through real sockets, exactly like a game client.

```mermaid
flowchart LR
    subgraph e2e["test/e2e — all-in-one"]
      C1[client TCP] & C2[client WS] --> S[gate+login+game<br/>LocalBus · memory]
    end
    subgraph cluster["test/cluster — distributed"]
      K1[client] --> G1[gate-1]
      K2[client] --> G2[gate-2]
      G1 & G2 <--> N[(embedded NATS)]
      N <--> L[login-1] & GA[game-1] & GB[game-2]
      G1 & G2 & L & GA & GB -.-> R[(miniredis)]
    end
```

## test/e2e

| Test | Covers |
|---|---|
| `TestFullGameFlow` | auth guard, account validation, heartbeat, login over TCP and WebSocket, mutual AOI visibility, starter bag, item use with level-up and pushes, unusable/insufficient items, world chat, movement push, anti-teleport, GM, wrong password, duplicate login kick, stale session ignored, save + unload on disconnect, leave push |
| `TestManyConcurrentPlayers` | 50 concurrent clients logging in and moving |
| `TestShutdownSavesPlayers` | `App.Stop` sends `KickPush(SERVER_SHUTDOWN)` and persists players |

## test/cluster

| Test | Covers |
|---|---|
| `TestDistributedCluster` | registry discovery via Redis protocol, players spread over two game nodes, world chat across nodes and gates, re-login through another gate lands on the same game node and kicks the old session, save to shared store |
| `TestLoginUnavailableWithoutGameNodes` | `SERVICE_UNAVAILABLE` when no game node is registered |

Every node in `test/cluster` has its own NATS connection and registry client,
so the code paths are identical to separate processes. Storage is shared via
`bootstrap.WithRepos` (stand-in for MongoDB).

```bash
go test ./test/...
go test -count=10 ./test/...       # flakiness check
go test -run TestDistributedCluster -v ./test/cluster/
```
