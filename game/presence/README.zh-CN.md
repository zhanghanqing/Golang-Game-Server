# presence —— 每个在线玩家归谁管

[English](README.md)

`game/presence` 记录**每个在线玩家当前由哪台游戏服保存在内存里**。
有了它，在游戏服扩容、排空、升级或宕机时，同一个玩家都不会同时被加载到两台机器上。

## 为什么只靠一致性哈希不够

登录服用 rendezvous 哈希在已注册的游戏服中选一台。只有节点集合不变时，这种选择才是"粘性"的：

```mermaid
sequenceDiagram
    autonumber
    participant C as 客户端 (ann)
    participant L as 登录服
    participant A as game-1
    participant B as game-2（新）
    Note over A: ann 正在 game-1 上在线
    Note over B: 自动扩容加入 game-2
    C->>L: 重连（第二台设备）
    L->>L: 对 {game-1, game-2} 做 hash(ann) → game-2
    L->>B: 把 ann 路由到 game-2
    B->>B: 从 MongoDB 加载 ann（旧数据）
    Note over A,B: ann 同时活在两台机器上，<br/>互相覆盖存档 ✗
```

有了归属目录，登录服会先问一句"ann 归谁？"：

```mermaid
sequenceDiagram
    autonumber
    participant C as 客户端 (ann)
    participant L as 登录服
    participant P as presence (Redis)
    participant A as game-1
    C->>L: 重连
    L->>P: Owner(ann)
    P-->>L: game-1
    L->>L: game-1 还活着吗（正常或排空中）？是
    L->>A: 把 ann 路由到 game-1
    A->>A: 还是内存里那个玩家，踢掉旧连接 ✓
```

## 数据

```
key:   gs:presence:<uid>      value: <游戏服节点 id>      ttl: 5 分钟，每次存盘周期刷新
```

| 操作 | 调用方 | 规则 |
|---|---|---|
| `Owner(uid)` | 登录服 | 返回持有该玩家的节点，没有则为 `""` |
| `Acquire(uid, node)` | 游戏服，加载玩家之前 | 空闲或本来就是自己时成功；否则返回另一个持有者 |
| `Takeover(uid, from, node)` | 游戏服 | 仅当当前持有者仍是 `from`（一个已死节点）时成功 |
| `Release(uid, node)` | 游戏服，最后一次存盘之后 | 仅当仍归自己时删除 |

每个 Redis 操作都是 Lua 实现的"比较并设置"，两台机器同时抢一个玩家不可能都成功
（见 `TestConcurrentAcquireHasOneWinner`）。

## 完整规则

```mermaid
flowchart TD
    A[登录服: Owner uid] -->|持有者存活<br/>正常或排空中| R1[路由到持有者]
    A -->|没有持有者或持有者已死| R2[只在正常节点上<br/>做 rendezvous 哈希]
    R1 & R2 --> E[游戏服: EnterGame]
    E --> Q{Acquire}
    Q -->|归自己| L[加载玩家，开始游戏]
    Q -->|别的存活节点持有| X[返回错误：仍在别处在线，<br/>客户端稍后重试]
    Q -->|别的已死节点持有| T[Takeover 接管] --> L
    L --> S[每个存盘周期：再次 Acquire<br/>= 刷新 TTL]
    S -->|归属已丢失| U[卸载且不存盘<br/>旧副本不能覆盖新数据]
    L --> O[下线：先存盘，再 Release]
```

* **存活**指在注册中心的 `game` 或 `game-draining` 下能找到该节点（`cluster.NodeAlive`）。
  判定"已死"之前会重新从 Redis 读一次，因为各进程的缓存是按 key 分别刷新的。
* **失败即拒绝**：读不到 Redis 时登录服返回 `SERVICE_UNAVAILABLE`，而不是瞎猜——猜错就可能出现第二份副本。
* **先 Release 再离开在线表**：下线过程中到达的请求会落到正在关闭的实体上，
  再被转给一个新实体，新实体会重新获取归属。

## 实现

| 类型 | 使用场景 |
|---|---|
| `presence.Memory` | 单体模式和单元测试（单进程） |
| `presence.Redis` | 分布式模式（`cluster.registry: redis`），所有登录服和游戏服共享 |

`bootstrap` 会自动选择。

## 测试

* `presence_test.go` —— 归属规则和并发抢占，分别针对内存和 Redis（miniredis）实现。
* `test/cluster/drain_test.go` —— 用真实 NATS 测试扩容、排空、停服和宕机接管。
