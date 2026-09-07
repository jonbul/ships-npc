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
| Enemy ship controller | Which fleets fly: `none`, `rule` (the hand-written controller, ships named `[NPC] ...`), `ai` (the neural network, ships named `[AI] ...`) or `both`. A disabled fleet despawns. |
| Enemy ships / AI ships | How many hostile Ship NPCs of each fleet hunt the players at once, `0`-`100` each. `0` disables that fleet; lowering it despawns the excess silently (no explosion, no kill-feed entry, since nobody killed them). |
| Enemy ship life | Starting/maximum life of a *newly spawned* ship. Ships already in play keep the value they spawned with. |
| Enemy ship speed | Cruising speed, in the game's **own speed units** (the same scale as ships-vue's `SPEED.MAX`). A player's top speed is `50`, so `50` means players can never outrun them. Default `20`. Escaping a black hole ignores this and always uses the full envelope, since being sucked in is fatal. |
| Enemy ship fire rate | Cooldown between a ship's shots, in ms. Each ship has its own cooldown, so they don't fire in lockstep. |
| Attack matrix | Six independent switches: whether each fleet (`[NPC]`, `[AI]`) may attack players, `[NPC]` ships and `[AI]` ships. A ship goes for whichever *permitted* target is nearest, so enabling a pair adds a target rather than distracting it from players, and a forbidden pair is neither hunted nor damaged. Only "attack players" is on by default, for both fleets. |
| Ships take damage when they touch | Ramming, for everyone: players and both fleets. Off means ships still push each other apart but take no damage. It ignores the attack matrix - a collision is not an attack - and ships-go also broadcasts it to every browser, since player contact is resolved client-side. On by default. |
| Ships grow with their score | A player's ship is resized by kills minus deaths, which also changes its collision box. Off by default. Enforced by the browser and broadcast by ships-go; nothing here reads it (NPC ships have a fixed scale) - it is carried in this struct only so it stays identical to ships-go's. |
| Ship size | The size every ship is normalised to, whatever its artwork measures - so a 400px hull and a 100px one meet as equals. `20`-`1000`, default `100` (the value ships-vue used to hardcode). Unlike the two rules above this one *is* applied here: the scale is a ship's collision box, so it decides how close a ram is, how big a target looks and how far apart two ships are held. Changing it re-scales the fleet already flying. |
| Max black holes | Cap on black holes alive at once. `0` stops new ones; existing ones still live out their duration. |
| Black hole spawn period | Delay between black hole spawns, in seconds. |
| Black hole duration | How long a black hole lives before it shrinks away, in seconds. Default `180`. Read every tick, not stamped on the black hole at spawn, so shortening it retires the ones already on the map. |

Values are clamped on both sides (`npcSettings.sanitized` here,
`NpcSettingsData.Sanitized` in ships-go): a zero fire rate would fire every
tick, and a zero spawn period would spawn a black hole every tick. The
defaults (`rule` controller, 1 ship per fleet, 10 life, speed 20, 500 ms
fire rate, both fleets attacking players only,
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
- `NPC_METRICS_INTERVAL_MS` (default `5000`): how often this service reports
  its own CPU and memory to ships-go (see Metrics below).

## Metrics

This service has no HTTP endpoint of its own: it is a websocket client on
loopback, so nothing could reach it to scrape it. Instead it samples its own
resource usage and pushes it to ships-go as an `npcMetrics` event, and
ships-go re-exports it on the `/metrics` endpoint Prometheus already scrapes,
as `ships_npc_cpu_seconds_total` (counter), `ships_npc_cpu_percent`,
`ships_npc_memory_resident_bytes`, `ships_npc_memory_heap_bytes`,
`ships_npc_memory_heap_sys_bytes`, `ships_npc_goroutines`,
`ships_npc_simulated_npcs`, `ships_npc_tick_seconds` and `ships_npc_up`.

`ships_npc_simulated_npcs` and `ships_npc_tick_seconds` are the pair to watch
when sizing fleets: a tick average approaching `NPC_TICK_INTERVAL_MS` is a
simulation about to fall behind, whatever the CPU percentage says.

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
  Any number can be simulated at once - up to 100 per fleet - each with its
  own target, throttle and fire cooldown, so they spread out and attack from
  different angles instead of behaving as one squadron.

## The two controllers

Every enemy ship is flown by one of two brains, chosen per fleet in the admin
panel, and its name says which: `[NPC] Falcon 7` or `[AI] Falcon 7`. Both
fleets can run at once, which is the point - it lets them be compared, or set
against each other through the attack matrix.

- **`rule`** - the hand-written controller in `ruleDecision`: pick a heading,
  turn towards it, accelerate while closing, brake inside standoff, fire when
  lined up.
- **`ai`** - a small neural network in `ai.go`: a 12-32-32-3 multilayer
  perceptron, about 1.5k parameters, evaluated in plain Go. No framework, no
  cgo, no GPU and no network call; 100 v 100 all-against-all costs about 6 ms
  of the 100 ms tick. Its weights live in `aiPolicy.json`, compiled into the
  binary with `go:embed` (**the file must exist or the build fails**); if they
  fail to load, those ships quietly fly the rules instead.

**Both brains only decide.** They return a `shipCommand{turn, thrust, fire}`
and `applyCommand` is the only thing that moves a ship, so an AI ship cannot
out-turn, out-accelerate or out-shoot a rule ship or a player no matter what
the network outputs. Keep it that way if you add a third controller.

### Retraining the policy

```bash
SHIPS_NPC_TRAIN=1 go test -run TestTrainAiPolicy -timeout 30m
```

It's *behaviour cloning*: the rule controller is a free teacher, so there's no
reward function to design and it converges in about 20 seconds (~75k samples,
turn MAE ~0.02, ~99% agreement on when to fire). The samples are collected
through the simulator's `observer` hook, so they come from the **exact** code
path production uses - that hook exists to prevent train/serve skew; don't
bypass it by generating features separately. The test rewrites
`aiPolicy.json`; commit it.

Two encoding rules in `features()` matter more than the architecture: angles
go in as **sin/cos pairs**, never raw radians (+179 deg and -179 deg are
nearly the same heading but the two furthest-apart numbers), and the black
hole goes in as **closeness rather than distance**, so "no black hole" and "a
very distant one" both read as 0.

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
| Player → NPC (ramming) | **Both.** The player's own client damages the player and reports the ship it rammed with an `npcHit` carrying an empty `bulletId`; this service applies that. Without it the collision would hurt only the player. |
| NPC → NPC (ramming) | **Here** (`ramEnemyShips`), for the same reason as NPC bullets: no browser is watching. Same damage and per-pair cooldown ships-vue uses, so a collision costs a ship exactly what it costs a player. |
| NPC → NPC | **Here.** No browser tracks it, so this service flies its own bullets (`activeBullets` keeps each one's trajectory) and resolves the hits in `advanceBullets`. Only active while the attack matrix permits that attacker/target pair. |

Because `NPC_TICK_INTERVAL_MS` is configurable, a bullet can cross more than
a ship's width in a single step, so hit detection sweeps the segment the
bullet covered during the tick rather than testing where it ended up. A ship
is exempt from its own bullets (they start inside its hull) and stops
avoiding the rival it is hunting, or the two just orbit each other and never
line up a shot.
