# logx — structured logging

[中文](README.zh-CN.md)

A thin setup layer over the standard library `log/slog`. Code everywhere simply
uses `*slog.Logger`; no third-party logging dependency leaks into modules.

```go
logx.Setup(logx.Config{Level: "info", Format: "json"}) // once in main
log := logx.Module("gate")                             // child logger with module=gate
log.Info("session opened", "sid", 42, "remote", addr)
```

| Config key | Values | Default |
|---|---|---|
| `level` | `debug` `info` `warn` `error` | `info` |
| `format` | `text` (human) / `json` (log pipelines: Loki, ELK, CloudWatch) | `text` |
| `add_source` | add `file:line` | `false` |

Output examples:

```
time=2026-10-20T10:00:00Z level=INFO msg="login ok" module=login account=alice uid=1234 game_node=game-2
{"time":"2026-10-20T10:00:00Z","level":"INFO","msg":"login ok","module":"login","account":"alice","uid":1234}
```

Conventions used across the project:

* always log key/value pairs, never formatted strings — they are searchable;
* `module` identifies the component; `uid`, `sid`, `msg_id`, `node` identify the subject;
* `debug` for per-request traces, `info` for lifecycle, `warn` for recoverable
  anomalies, `error` for failures that need attention.
