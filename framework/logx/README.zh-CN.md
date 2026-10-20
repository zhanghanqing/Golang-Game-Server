# logx —— 结构化日志

[English](README.md)

基于标准库 `log/slog` 的一层薄封装。所有代码直接使用 `*slog.Logger`，各模块不会依赖任何第三方日志库。

```go
logx.Setup(logx.Config{Level: "info", Format: "json"}) // 在 main 中调用一次
log := logx.Module("gate")                             // 带 module=gate 标签的子 logger
log.Info("session opened", "sid", 42, "remote", addr)
```

| 配置项 | 取值 | 默认 |
|---|---|---|
| `level` | `debug` `info` `warn` `error` | `info` |
| `format` | `text`（人读）/ `json`（日志系统：Loki、ELK、CloudWatch） | `text` |
| `add_source` | 附带 `文件:行号` | `false` |

输出示例：

```
time=2026-10-20T10:00:00Z level=INFO msg="login ok" module=login account=alice uid=1234 game_node=game-2
{"time":"2026-10-20T10:00:00Z","level":"INFO","msg":"login ok","module":"login","account":"alice","uid":1234}
```

项目中的约定：

* 一律使用键值对，不用格式化字符串 —— 便于检索；
* `module` 标识组件；`uid`、`sid`、`msg_id`、`node` 标识对象；
* `debug` 用于逐请求跟踪，`info` 用于生命周期事件，`warn` 用于可恢复异常，`error` 用于需要人工关注的失败。
