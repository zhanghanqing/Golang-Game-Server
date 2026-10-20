# test —— 集成测试

[English](README.md)

单元测试和各自的包放在一起。这个目录里的测试会启动**完整的服务器**，并像真正的游戏客户端一样通过真实 socket 与之通信。

```mermaid
flowchart LR
    subgraph e2e["test/e2e —— 单体"]
      C1[TCP 客户端] & C2[WS 客户端] --> S[gate+login+game<br/>LocalBus · 内存]
    end
    subgraph cluster["test/cluster —— 分布式"]
      K1[客户端] --> G1[gate-1]
      K2[客户端] --> G2[gate-2]
      G1 & G2 <--> N[(内嵌 NATS)]
      N <--> L[login-1] & GA[game-1] & GB[game-2]
      G1 & G2 & L & GA & GB -.-> R[(miniredis)]
    end
```

## test/e2e

| 测试 | 覆盖内容 |
|---|---|
| `TestFullGameFlow` | 鉴权拦截、账号校验、心跳、TCP 与 WebSocket 登录、AOI 互相可见、新手背包、使用道具升级并推送、不可用/数量不足的道具、世界聊天、移动推送、防瞬移、GM、密码错误、顶号踢人、忽略过期会话、断线存盘并卸载、离开推送 |
| `TestManyConcurrentPlayers` | 50 个并发客户端登录并移动 |
| `TestShutdownSavesPlayers` | `App.Stop` 发送 `KickPush(SERVER_SHUTDOWN)` 并持久化玩家 |

## test/cluster

| 测试 | 覆盖内容 |
|---|---|
| `TestDistributedCluster` | 通过 Redis 协议进行服务发现、玩家分布在两个游戏节点、跨节点跨网关的世界聊天、从另一个网关重登会落到同一游戏节点并踢掉旧会话、保存到共享存储 |
| `TestLoginUnavailableWithoutGameNodes` | 没有注册游戏节点时返回 `SERVICE_UNAVAILABLE` |

`test/cluster` 中每个节点都有自己的 NATS 连接和注册中心客户端，因此代码路径与独立进程完全一致。
存储通过 `bootstrap.WithRepos` 共享（代替 MongoDB）。

## test/webconsole —— 浏览器测试（无头 Chrome）

构建服务器镜像并启动，然后用 Puppeteer 像真实用户一样操作 [网页控制台](../game/webconsole/README.zh-CN.md)：
共 28 项检查，覆盖一键自检、通过按钮完成的手动流程、两个浏览器在地图上相遇并聊天、键盘和点击地图移动、
顶号踢人及界面重置、可读的错误提示、密码打码、语言切换、390 像素手机布局，以及零 JavaScript 错误。
截图和 `results.json` 输出到 `test/webconsole/out/`。

```bash
docker compose -f test/webconsole/compose.yml up --build --abort-on-container-exit --exit-code-from browser
docker compose -f test/webconsole/compose.yml down
```

## 命令

```bash
go test ./test/...
go test -count=10 ./test/...       # 检查是否偶发失败
go test -run TestDistributedCluster -v ./test/cluster/
```
