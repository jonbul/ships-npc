# ships-npc

NPC controller service for the [Ships game](https://github.com/jonbul/ships-vue).

This service owns all NPC simulation (currently: black holes, and a
destructible enemy Ship NPC that fights players). It doesn't expose any
HTTP/WS server itself — instead it connects to the
[`ships-go`](https://github.com/jonbul/ships-go) backend's `/ws`
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
4. It receives `npcConfig` events carrying the NPC settings an admin chose
   in ships-vue's admin panel (see below). ships-go sends one whenever the
   settings are saved, and one right after `npcAuth` succeeds, so a restart
   of either process converges on the same values.

> **Only one ships-npc may run at a time.** Every `npcUpdate` replaces the
> entire NPC snapshot, so two instances do not add up — they overwrite each
> other every tick, and players see enemy ships flicker in and out of
> existence: unnamed, undrawn and impossible to hit, while their bullets keep
> arriving. ships-go therefore accepts only the first controller to
> authenticate and answers any duplicate with an `npcRejected` event, which
> this service logs. If you see that in the log, a previous `ships-npc` is
> almost certainly still running.

## Admin settings

The NPCs are tuned live from ships-vue's admin panel. The browser never
talks to this service: ships-go holds the values and relays them over the
connection above, which is why there's no HTTP server, port or credential
here. Every setting is applied on the next tick — nothing restarts, no NPC
respawns.

| Setting | Effect |
| --- | --- |
| Enemy ships | How many hostile Ship NPCs hunt the players at once. `0` disables them; lowering it despawns the excess silently (no explosion, no kill-feed entry, since nobody killed them). |
| Enemy ship life | Starting/maximum life of a *newly spawned* ship. Ships already in play keep the value they spawned with. |
| Enemy ship speed | Cruising speed, in the game's **own speed units** (the same scale as ships-vue's `SPEED.MAX`). A player's top speed is `50`, so `50` means players can never outrun them. Default `20`. Escaping a black hole ignores this and always uses the full envelope, since being sucked in is fatal. |
| Enemy ship fire rate | Cooldown between a ship's shots, in ms. Each ship has its own cooldown, so they don't fire in lockstep. |
| Enemy ships attack each other | When on, ships treat each other as targets as well as players and go for whichever is nearest — so it adds a target rather than distracting them from players. Off by default. |
| Max black holes | Cap on black holes alive at once. `0` stops new ones; existing ones still live out their duration. |
| Black hole spawn period | Delay between black hole spawns, in seconds. |

Values are clamped on both sides (`npcSettings.sanitized` here,
`NpcSettingsData.Sanitized` in ships-go): a zero fire rate would fire every
tick, and a zero spawn period would spawn a black hole every tick. The
defaults (1 ship, 10 life, speed 20, 500 ms fire rate, no infighting,
2 black holes, 30 s spawn period) are duplicated in both services and **must be kept in agreement**,
since they're what runs before an admin ever opens the panel.

## Env vars

- `NPC_SECRET` (required): must match `NPC_SECRET` on the `ships-go` side.
- `NPC_WS_URL` (default `wss://localhost:3000/ws`): ships-go websocket URL.
- `NPC_API_URL` (default: derived from `NPC_WS_URL`, e.g.
  `https://localhost:3000`): ships-go HTTP base URL, used to fetch public
  ships via `GET /game/getShips` for the enemy Ship NPC. Fetched with a few
  retries at startup; if it never succeeds the enemy ship simply stays
  disabled (black holes are unaffected).
- `NPC_TLS_INSECURE_SKIP_VERIFY` (default `true`): skip TLS verification,
  useful for ships-go's local self-signed dev certificate.
- `NPC_TICK_INTERVAL_MS` (default `100`): simulation tick interval.

## Run

```
go run .
```

Run this alongside `ships-go` (same host, so the localhost check passes).

## NPC kinds

- **Black hole**: spawns, grows/shrinks, moves, expires, pulls/kills nearby
  ships on contact. Purely a hazard, not destructible. Its gravity is
  applied *twice, independently*: ships-vue applies it to the local player,
  and ships-npc applies the same maths to its own NPC ships. Neither is
  authoritative over the other; each side just moves what it owns. The
  constants in `npc.go` (`blackHolePull*`) are mirrored from ships-vue's
  `applyNpcImpacts()` and are expressed *per client frame*, so they're
  rescaled by `pullScale` to whatever `NPC_TICK_INTERVAL_MS` is set to -
  keep them in sync if the frontend's ever change.
- **Enemy Ship** (`type: "Ship"`): picks a random ship from
  `GET /game/getShips`, spawns it as a hostile "player-like" entity that
  chases the nearest tracked player and fires bullets at it (reusing the
  existing `newBullet`/`removeBullet` events - ships-npc is authoritative for
  its own bullets' lifetime via a fixed timer, since no client "owns" them).
  It's destructible: the frontend detects the player's own bullets hitting
  it and sends the new `npcHit` event; `ships-npc` applies the damage, and
  on death announces it via a `playerDied`-shaped message (crediting the
  killer through ships-go's existing, unmodified kill-feed logic) and
  respawns it after a short delay. It's also subject to black hole gravity
  and will break off its attack to escape a nearby one (and can be killed
  by it, announced the same way but with no killer, so nobody is credited).
  See constants at the top of `npc.go` for all tunable values (speed
  factors, shoot range/cooldown, health, respawn delay, fear radius).

  It aims at the **middle** of its target, not at the top-left anchor that
  travels on the wire. A player's ship geometry comes from the `width`,
  `height` and `scale` their client sends: `GET /game/getShips` lists only
  the *public* ships, so a player flying one of their own painting projects
  cannot be looked up there at all, and guessing produced aim errors of
  hundreds of pixels. `scale` doesn't move a ship's center (the client
  compensates with `xTranslation`) but it is the drawn size, so it is what
  sizes the aim tolerance.
  Its speed model is **derived from ships-vue's `SPEED` constants**, not
  invented: it cruises at a fraction (`enemyShipSpeedFactor`) of a player's
  top speed using a player's acceleration, rescaled from the client's 30fps
  frames to this service's tick rate. Keep them in sync - hardcoded values
  previously left it ~6x slower than a player, which made it look broken
  (trailing forever at a fixed bearing, never reaching firing range).
  Currently only one enemy ship is simulated at a time (the wire protocol
  already supports multiple; extending to a map of ships in `npc.go` would
  be needed for more).

## Adding a new NPC kind

Add spawn/update logic in `npc.go` (see `spawnBlackHole` / `npcSimulator.tick`
for the pattern) producing `NpcData` values with a distinct `Type`. Purely
cosmetic/non-destructible NPCs need no protocol or ships-go changes - the
frontend (`ships-vue`) already dispatches NPC behavior by `type`. Destructible
NPCs additionally need: frontend collision detection sending the `npcHit`
event (see `checkEnemyShipBulletCollision` in `ships-vue`'s `game.js`), and a
handler in `ships-npc` for that event (see `handleNpcHit` in `npc.go`).

## Who resolves which hit

Damage is deliberately resolved by whoever can actually see it, and there is
one case only this service can:

| Shooter → target | Resolved by |
| --- | --- |
| Player → NPC | The shooting player's client, which is the only thing tracking that bullet. It sends `npcHit`; ships-go relays it here (`handleNpcHit`). |
| NPC → player | The victim's own client, exactly as for a player's bullet (`checkEnemyShipBulletCollision`). |
| NPC → NPC | **Here.** No browser tracks it, so this service flies its own bullets (`activeBullets` keeps each one's trajectory) and resolves the hits in `advanceBullets`. Only active while `enemyShipsFightEachOther` is on. |

Because `NPC_TICK_INTERVAL_MS` is configurable, a bullet can cross more than
a ship's width in a single step, so hit detection sweeps the segment the
bullet covered during the tick rather than testing where it ended up. A ship
is exempt from its own bullets (they start inside its hull) and stops
avoiding the rival it is hunting, or the two just orbit each other and never
line up a shot.
