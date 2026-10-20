# gamesvc —— 游戏服务

[English](README.md)

游戏服承载**在线玩家**和**世界场景**。它是有状态的：每个玩家只存在于一个游戏节点上，
该节点在登录时通过 uid 的 HRW 哈希选定，之后网关会把该玩家的所有请求都路由到这个节点（粘性路由）。

## 线程模型

```mermaid
flowchart LR
    BUS[总线回调<br/>gs.node.game.&lt;node&gt;] -- "FORWARD uid=42" --> D[dispatch]
    D -- getOrCreate(42) --> PM[(在线表<br/>uid → *Player)]
    D -- Post --> PA42
    subgraph PA42[玩家 actor 42]
      H[处理器] --> ST[(data · bag · session)]
    end
    subgraph PA7[玩家 actor 7]
      H7[处理器]
    end
    PA42 -- "Scene.Move()（投递）" --> SA
    PA7 -- "Scene.Enter()" --> SA
    subgraph SA[场景 actor]
      G[(AOI 网格 · 实体表)]
    end
    SA -- "router.Push(session, …)" --> GATES[网关]
    PA42 -- "router.Broadcast(…)" --> GATES
```

* **每个在线玩家一个 actor**：该玩家的所有处理器在其中顺序执行，玩家状态无需加锁。
* **每个场景一个 actor**：拥有 AOI 网格和实体表；玩家通过投递（`Enter`、`Leave`、`Move`、`BroadcastNearby`）与之交互。
* 总线回调只负责找到 actor 并投递 —— 永不阻塞。

## 玩家生命周期

```mermaid
stateDiagram-v2
    [*] --> Created: 该 uid 的第一个 FORWARD
    Created --> Loaded: EnterGameReq → 从仓储加载（或创建新角色）
    Created --> Closed: 其他请求 → ERR_NOT_IN_GAME，释放实体
    Loaded --> Loaded: 来自绑定会话的请求<br/>定时保存（有脏数据时）
    Loaded --> Loaded: 新会话的 EnterGameReq<br/>踢掉旧会话（DUPLICATE_LOGIN）
    Loaded --> Closed: SESSION_CLOSED（会话匹配）<br/>或服务停止
    Closed --> [*]: 离开场景 · 保存 · 移除 · 停止 actor
```

关键细节：

* **会话校验** —— 除 `EnterGameReq` 外，所有处理器都要求请求来自进入游戏时绑定的会话；
  来自过期会话的请求返回 `ERR_NOT_IN_GAME`。
* **顶号** —— 已加载的玩家收到新的 `EnterGameReq` 时，会踢掉旧会话。旧会话随后发来的
  `SESSION_CLOSED` 因会话 ID 不匹配而被忽略。
* **下线竞态** —— 如果请求投递给了一个在处理前就已下线的玩家，该请求会被重新分发到新的玩家实例
  （通过 `p.closed` 判断），不会丢失。
* **持久化** —— 处理器设置 `p.dirty`；actor 定时器每 `save_interval` 保存一次；下线和停服时同步保存。
  每次保存 `Version` 自增。

## 处理器

| 请求 | 行为 | 错误 |
|---|---|---|
| `EnterGameReq{name}` | 加载/创建、绑定会话、回复，然后进入场景 | 存储失败 |
| `BagGetReq` | 返回所有道具，按 ID 排序 | |
| `ItemUseReq{item_id,count}` | 扣除道具，按配置表效果加金币/经验，推送 `BagUpdatePush` + `PlayerInfoPush` | `ERR_ITEM_NOT_FOUND/NOT_USABLE/NOT_ENOUGH` |
| `ChatSendReq{channel,text}` | 世界频道：通过所有网关广播；场景频道：发给 AOI 邻居 | `ERR_CHAT_EMPTY/TOO_LONG/TOO_FAST` |
| `SceneMoveReq{target}` | 校验边界与步长 ≤ `max_move_step`，更新位置，通知场景 | `ERR_POSITION_OUT_OF_BOUNDS`、`ERR_MOVE_TOO_FAR` |
| `GmCommandReq{command}` | `help`、`additem id n`、`addgold n`、`addexp n`、`info` | `ERR_GM_DISABLED`、`ERR_GM_BAD_COMMAND` |

`EnterGameResp` 在玩家进入场景**之前**发出，因此客户端一定先收到它，再收到场景推送。

## 场景与 AOI

世界大小 1000×1000，格子 100×100（`AOICellSize`）。玩家能看到自己周围 3×3 个格子
（见 [logic/aoi](../logic/README.zh-CN.md)）。

```mermaid
sequenceDiagram
    participant A as 玩家 A 的 actor
    participant S as 场景 actor
    participant B as 客户端 B（观察者）
    participant C as 客户端 C（新邻居）
    A->>S: Move(A, x, y)
    S->>S: grid.Move → stayed, entered, left
    S-->>B: SceneEntityMovePush(A)          （仍可见）
    S-->>C: SceneEntityEnterPush([A])        （新进入视野）
    S-->>A: SceneEntityEnterPush([C])        （A 看到 C）
    Note over S: left：向他们发 LeavePush(A)，向 A 发 LeavePush(left)
```

## 配置

```yaml
game:
  save_interval: 30s   # 定时保存脏数据
  gm_enabled: false    # 生产环境绝不能为 true
  chat_interval: 1s    # 同一玩家两次聊天的最小间隔
  max_move_step: 50    # 防瞬移：每次 SceneMoveReq 的最大移动距离
```

## 文件

| 文件 | 内容 |
|---|---|
| `service.go` | 模块生命周期、信封路由、玩家表、下线 |
| `player.go` | `Player` 实体：加载/创建、快照、保存、推送 |
| `scene.go` | 场景 actor、基于 AOI 的推送 |
| `handlers.go` | 所有请求处理器 + `handle` 适配器 |
