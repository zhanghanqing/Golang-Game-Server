# proto —— 协议定义

[English](README.md) · [协议规范](../docs/protocol.zh-CN.md)

所有客户端↔服务端、服务端↔服务端的消息都以 Protocol Buffers（proto3）定义在这里。
生成的 Go 代码已提交到仓库，因此不安装任何代码生成器也能直接 `go build`。

```
proto/
├── framework/
│   ├── sys.proto       → framework/pb/syspb       ID 1-999：心跳、错误、踢人、错误码
│   └── cluster.proto   → framework/pb/clusterpb   节点间内部信封 Envelope
└── game/
    ├── msgid.proto     → game/pb   MsgID 枚举（所有游戏消息 ID）+ ErrCode 枚举
    ├── login.proto     → game/pb   1001-1002
    ├── player.proto    → game/pb   2001-2003
    ├── bag.proto       → game/pb   2101-2105
    ├── chat.proto      → game/pb   2201-2203
    ├── scene.proto     → game/pb   2301-2305
    └── gm.proto        → game/pb   2901-2902
```

## 生成代码

需要 [`buf`](https://buf.build) 和 `protoc-gen-go`（不需要 `protoc`）：

```bash
go install github.com/bufbuild/buf/cmd/buf@v1.47.2
go install google.golang.org/protobuf/cmd/protoc-gen-go@v1.36.5
buf generate          # 在仓库根目录执行；配置见 buf.yaml / buf.gen.yaml
```

`buf.gen.yaml` 使用 `module=gameserver`，输出会落到每个文件 `go_package` 指定的包中。

## 约定

| 规则 | 原因 |
|---|---|
| 请求以 `Req` 结尾，响应以 `Resp` 结尾，服务端推送以 `Push` 结尾 | 日志易读、方向一目了然 |
| 响应 ID = 请求 ID + 1 | 好记、好调试 |
| 所有 ID 都写在 `MsgID` 枚举中 | 客户端的唯一事实来源 |
| 绝不复用或重新编号消息 ID、字段编号 | 老客户端会误解数据 |
| 新增字段而不是修改类型；删除的字段编号要 reserved | 保持 protobuf 线上兼容 |
| 错误码 ≥ 1000，写在 `ErrCode` 中并按系统分组 | 客户端把错误码映射为本地化文本 |

## ID 区间与路由

| 区间 | 服务 | 需要登录 | 路由方式 |
|---|---|---|---|
| 1–999 | 框架（心跳由网关处理） | — | — |
| 1000–1999 | login | 否 | 负载均衡 |
| 2000–9999 | game | 是 | 粘性 |

区间由 `game/protocol/protocol.go` 中的网关路由表强制执行。

## 客户端代码生成

客户端使用同一批文件生成代码，例如：

```bash
protoc --csharp_out=Assets/Scripts/Proto proto/framework/sys.proto proto/game/*.proto   # Unity
protoc --cpp_out=Source/Proto ...                                                     # Unreal
npx buf generate --template buf.gen.ts.yaml                                            # TypeScript
```
