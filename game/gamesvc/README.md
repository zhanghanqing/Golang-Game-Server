# gamesvc — game service

[中文](README.zh-CN.md)

The game service hosts **online players** and the **world scene**. It is
stateful: a player lives on exactly one game node, chosen at login by
rendezvous hashing of the uid, and the gate routes all of that player's
requests to that node (sticky routing).

## Threading model

```mermaid
flowchart LR
    BUS[bus callback<br/>gs.node.game.&lt;node&gt;] -- "FORWARD uid=42" --> D[dispatch]
    D -- getOrCreate(42) --> PM[(online map<br/>uid → *Player)]
    D -- Post --> PA42
    subgraph PA42[Player actor 42]
      H[handler] --> ST[(data · bag · session)]
    end
    subgraph PA7[Player actor 7]
      H7[handler]
    end
    PA42 -- "Scene.Move()  (Post)" --> SA
    PA7 -- "Scene.Enter()" --> SA
    subgraph SA[Scene actor]
      G[(AOI grid · entities)]
    end
    SA -- "router.Push(session, …)" --> GATES[gates]
    PA42 -- "router.Broadcast(…)" --> GATES
```

* One **actor per online player**: all handlers of that player run sequentially
  in it, so player state needs no locks.
* One **actor per scene**: owns the AOI grid and entity table; players talk to
  it by posting (`Enter`, `Leave`, `Move`, `BroadcastNearby`).
* The bus callback only looks up the actor and posts — it never blocks.

## Player lifecycle

```mermaid
stateDiagram-v2
    [*] --> Created: first FORWARD for uid
    Created --> Loaded: EnterGameReq → load from repo (or create new character)
    Created --> Closed: any other request → ERR_NOT_IN_GAME, entity released
    Loaded --> Loaded: requests from the bound session<br/>periodic save (dirty)
    Loaded --> Loaded: EnterGameReq from new session<br/>kick old session (DUPLICATE_LOGIN)
    Loaded --> Closed: SESSION_CLOSED (matching session)<br/>or service Stop
    Closed --> [*]: leave scene · save · remove · stop actor
```

Key details:

* **Session check** — every handler except `EnterGameReq` requires the request
  to come from the session bound at enter time. Requests from stale sessions
  get `ERR_NOT_IN_GAME`.
* **Duplicate login** — a new `EnterGameReq` for a loaded player kicks the old
  session. The old session's `SESSION_CLOSED` is ignored because its session id
  no longer matches.
* **Logout race** — if a request is posted to a player that logs out before
  processing it, the request is re-dispatched to a fresh player instance
  (`p.closed` check), so nothing is lost.
* **Persistence** — handlers set `p.dirty`; an actor timer saves every
  `save_interval`; logout and service shutdown save synchronously. `Version`
  increments on each save.

## Handlers

| Request | Behaviour | Errors |
|---|---|---|
| `EnterGameReq{name}` | load/create, bind session, reply, then enter scene | storage failure |
| `BagGetReq` | all stacks sorted by item id | |
| `ItemUseReq{item_id,count}` | consume, apply table effects (gold/exp), push `BagUpdatePush` + `PlayerInfoPush` | `ERR_ITEM_NOT_FOUND/NOT_USABLE/NOT_ENOUGH` |
| `ChatSendReq{channel,text}` | world: broadcast through every gate; scene: AOI neighbours | `ERR_CHAT_EMPTY/TOO_LONG/TOO_FAST` |
| `SceneMoveReq{target}` | validate bounds and step ≤ `max_move_step`, update, notify scene | `ERR_POSITION_OUT_OF_BOUNDS`, `ERR_MOVE_TOO_FAR` |
| `GmCommandReq{command}` | `help`, `additem id n`, `addgold n`, `addexp n`, `info` | `ERR_GM_DISABLED`, `ERR_GM_BAD_COMMAND` |

`EnterGameResp` is sent **before** the player enters the scene, so clients
always receive it before any scene push.

## Scene and AOI

The world is 1000×1000 with 100×100 cells (`AOICellSize`). Players see the 3×3
cells around them (see [logic/aoi](../logic/README.md)).

```mermaid
sequenceDiagram
    participant A as Player A actor
    participant S as Scene actor
    participant B as Client B (watcher)
    participant C as Client C (new neighbour)
    A->>S: Move(A, x, y)
    S->>S: grid.Move → stayed, entered, left
    S-->>B: SceneEntityMovePush(A)          (stayed)
    S-->>C: SceneEntityEnterPush([A])        (entered)
    S-->>A: SceneEntityEnterPush([C])        (A sees C)
    Note over S: left: LeavePush(A) to them, LeavePush(left) to A
```

## Configuration

```yaml
game:
  save_interval: 30s   # periodic save of dirty players
  gm_enabled: false    # NEVER true in production
  chat_interval: 1s    # min time between two chat messages per player
  max_move_step: 50    # anti-teleport: max distance per SceneMoveReq
```

## Files

| File | Content |
|---|---|
| `service.go` | module lifecycle, envelope routing, player map, logout |
| `player.go` | `Player` entity: load/create, snapshot, save, push |
| `scene.go` | scene actor, AOI-driven pushes |
| `handlers.go` | all request handlers + `handle` adapter |
