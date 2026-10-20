# config —— YAML 配置

[English](README.md)

框架不强制一个全局配置结构体。每个组件暴露自己的 `Config` 类型（`gate.Config`、`cluster.Config`……），
由应用把它们组合成一个根结构体（见 `game/bootstrap.Config`）。

```mermaid
flowchart LR
    F[configs/game.yaml] --> E[ExpandEnv<br/>${NAME:-默认值}] --> Y[yaml.v3 解码<br/>KnownFields = true] --> V{Validate&#40;&#41; 出错?}
    V -- 正常 --> S[根 Config 结构体]
    V -- 出错 --> X[启动时退出]
```

```go
var cfg bootstrap.Config
if err := config.Load("configs/game.yaml", &cfg); err != nil { ... }
```

## 特性

| 特性 | 说明 |
|---|---|
| 环境变量 | 文件中任意位置可写 `${NAME}` 或 `${NAME:-默认值}`，解析前展开。同一份文件既能本机用也能容器用。 |
| 严格字段 | 未知字段直接报错 —— 写错成 `idle_timout` 会在启动时失败，而不是悄悄用默认值 |
| 校验钩子 | 根结构体若实现了 `Validate() error`，解码后会自动调用 |
| 时长 | `30s`、`5m`、`1h30m` 可直接解码为 `time.Duration` |

## 示例

```yaml
node:
  id: ${NODE_ID:-game-1}
cluster:
  nats_url: ${NATS_URL:-nats://127.0.0.1:4222}
game:
  save_interval: 30s
```

```bash
NODE_ID=game-7 NATS_URL=nats://nats:4222 ./server -config configs/game.yaml
```

密码、令牌等敏感信息应由编排系统通过环境变量注入，绝不要提交到 YAML 中。
