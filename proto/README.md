# proto — protocol definitions

[中文](README.zh-CN.md) · [Protocol spec](../docs/protocol.md)

All client/server and server/server messages are defined here in Protocol
Buffers (proto3). Generated Go code is committed so a plain `go build` works
without any code generator installed.

```
proto/
├── framework/
│   ├── sys.proto       → framework/pb/syspb       ids 1-999: heartbeat, error, kick, error codes
│   └── cluster.proto   → framework/pb/clusterpb   internal Envelope between nodes
└── game/
    ├── msgid.proto     → game/pb   MsgID enum (all game ids) + ErrCode enum
    ├── login.proto     → game/pb   1001-1002
    ├── player.proto    → game/pb   2001-2003
    ├── bag.proto       → game/pb   2101-2105
    ├── chat.proto      → game/pb   2201-2203
    ├── scene.proto     → game/pb   2301-2305
    └── gm.proto        → game/pb   2901-2902
```

## Generating code

Requires [`buf`](https://buf.build) and `protoc-gen-go` (no `protoc` needed):

```bash
go install github.com/bufbuild/buf/cmd/buf@v1.47.2
go install google.golang.org/protobuf/cmd/protoc-gen-go@v1.36.5
buf generate          # from the repository root; config in buf.yaml / buf.gen.yaml
```

`buf.gen.yaml` uses `module=gameserver`, so output lands in the package named by
each file's `go_package`.

## Conventions

| Rule | Reason |
|---|---|
| Requests end with `Req`, responses with `Resp`, server pushes with `Push` | readable logs, obvious direction |
| Response id = request id + 1 | easy to remember, easy to debug |
| Every id lives in the `MsgID` enum | single source of truth for clients |
| Never reuse or renumber ids or field numbers | old clients would misinterpret data |
| Add fields instead of changing types; reserve removed field numbers | protobuf wire compatibility |
| Error codes ≥ 1000 in `ErrCode`, grouped by system | clients map codes to localized text |

## Id ranges and routing

| Range | Service | Auth | Routing |
|---|---|---|---|
| 1–999 | framework (gate handles heartbeat) | — | — |
| 1000–1999 | login | no | load-balanced |
| 2000–9999 | game | yes | sticky |

The ranges are enforced by the gate route table in `game/protocol/protocol.go`.

## Client generation

Clients generate code from the same files, e.g.:

```bash
protoc --csharp_out=Assets/Scripts/Proto proto/framework/sys.proto proto/game/*.proto   # Unity
protoc --cpp_out=Source/Proto ...                                                     # Unreal
npx buf generate --template buf.gen.ts.yaml                                            # TypeScript
```
