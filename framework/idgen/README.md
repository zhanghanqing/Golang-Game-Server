# idgen — Snowflake ids

[中文](README.zh-CN.md)

Globally unique, roughly time-ordered 63-bit integers without a central
database — used for player uids (and suitable for item instances, mails,
orders, guilds…).

```
 63        62                       22            12             0
 ┌─────────┬────────────────────────┬─────────────┬──────────────┐
 │ 0 (sign)│ 41 bits ms since Epoch │ 10 bits     │ 12 bits      │
 │         │ (~69 years)            │ worker id   │ sequence     │
 └─────────┴────────────────────────┴─────────────┴──────────────┘
 Epoch = 2024-01-01 UTC   1024 workers   4096 ids / ms / worker
```

```go
g, _ := idgen.NewSnowflake(cfg.Node.WorkerID) // 0..1023, unique per process
uid := g.Next()
t, worker, seq := idgen.Parse(uid)            // debugging
```

| Situation | Behaviour |
|---|---|
| > 4096 ids in one ms | borrows the next millisecond (logical clock), never blocks or duplicates |
| clock moves backwards (NTP) | keeps using the last timestamp; ids stay unique and increasing |
| two processes with the same worker id | **duplicates possible** — assign `worker_id` per process (config / env `WORKER_ID`) |

Why not UUIDs? 64-bit integers are compact in protobuf (varint), index well in
databases, sort by creation time, and are readable in logs.
