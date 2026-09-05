# ships-npc

NPC controller service for the [Ships game](https://github.com/jonbul/ships-vue).

This service owns all NPC simulation (currently: black holes, more NPC kinds
planned). It doesn't expose any HTTP/WS server itself — instead it connects
to the [`ships-go`](https://github.com/jonbul/ships-go) backend's `/ws`
endpoint as a websocket **client**, the same way a player's browser does.

## How it talks to ships-go

1. On connect, it sends an `npcAuth` event with a shared secret
   (`NPC_SECRET`). ships-go only accepts this from a loopback connection
   (i.e. `ships-npc` must run on the same host as `ships-go`) and with a
   matching `NPC_SECRET` env var on the ships-go side.
2. It listens to the regular `gameBroadcast` messages ships-go sends to every
   connected socket, and accumulates player positions from them exactly like
   the game client does (incremental `players` updates + `activePlayerIds`
   pruning). This is used to decide where/when to spawn NPCs.
3. On every simulation tick, it sends a single `npcUpdate` event containing
   the **full current batch** of NPCs (not deltas). This is the most
   efficient approach for any number of NPCs: one message, one write, per
   tick, regardless of NPC count. ships-go simply stores and relays this
   snapshot to players as `npcs` in its own `gameBroadcast` payload — it does
   not simulate anything itself anymore.

## Env vars

- `NPC_SECRET` (required): must match `NPC_SECRET` on the `ships-go` side.
- `NPC_WS_URL` (default `wss://localhost:3000/ws`): ships-go websocket URL.
- `NPC_TLS_INSECURE_SKIP_VERIFY` (default `true`): skip TLS verification,
  useful for ships-go's local self-signed dev certificate.
- `NPC_TICK_INTERVAL_MS` (default `100`): simulation tick interval.

## Run

```
go run .
```

Run this alongside `ships-go` (same host, so the localhost check passes).

## Adding a new NPC kind

Add spawn/update logic in `npc.go` (see `spawnBlackHole` / `npcSimulator.tick`
for the pattern) producing `NpcData` values with a distinct `Type`. No
protocol or ships-go changes are needed — the frontend (`ships-vue`) already
dispatches NPC behavior by `type`.
