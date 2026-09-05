CHANGES
=======
Version 0.4.1 - 2026-09-05
------------------
- Enemy ship speed is now expressed in the game's own speed units (the same
  scale as ships-vue's `SPEED.MAX`) instead of a 0-1 fraction of a player's
  top speed, so the admin panel shows a number that means something in game
  terms. `50` is exactly a player at full throttle.
- New defaults: 1 enemy ship, 10 life, speed 20, 500 ms fire rate, 2 black
  holes, 30 s spawn period. Ships are slower (20 against a player's 50) but
  shoot 3x more often, and black holes are much rarer.
- `TestCatchesAFleeingPlayer` no longer claims a ship can catch a player at
  full throttle - with the new default speed it can't, by design. It now
  pins that the ship reaches its *configured* speed (the actual regression
  it was written for), and a new `TestAPlayerAtFullThrottleCanEscape` pins
  the escape hatch the default leaves open.

Version 0.4.0 - 2026-09-05
------------------
- Any number of enemy Ship NPCs can now be simulated at once, instead of
  exactly one. Each ship is simulated independently - its own target, its
  own throttle and its own fire cooldown - rather than as a squadron, so
  they naturally spread out and attack from different angles, and no
  single ship's decision can stall the others. The wire protocol already
  supported this (the whole NPC batch travels in one message keyed by id,
  and ships-vue reconciles by absence), so nothing had to change in the
  protocol to make it work.
- NPC settings are now controlled live from ships-vue's admin panel: how
  many enemy ships there are, their life, their speed (as a fraction of a
  player's top speed) and their fire rate, plus the black hole cap and
  spawn period. ships-go owns the values - the browser can't reach this
  service - and pushes them down the existing websocket as an `npcConfig`
  event, both when an admin saves and right after this service
  authenticates. That means no new port, endpoint or credential here, and
  either process can restart without the two drifting apart. Changes take
  effect on the next tick: nothing restarts and no NPC respawns.
- Lowering the enemy ship count despawns the excess silently, with no
  `playerDied`: nobody killed them, so an explosion and a kill-feed entry
  would be a lie. The respawn delay is only armed by an actual death, so
  raising the count adds ships immediately.
- Incoming settings are clamped (npcSettings.sanitized). A zero fire rate
  would otherwise fire every tick and a zero black hole spawn period would
  spawn one every tick.

Version 0.3.1 - 2026-09-05
------------------
- Bugfix: the enemy ship appeared to stop rotating and stop shooting once
  it had approached a player. It was actually still aiming perfectly - it
  was just ~6x slower than a player (60 px/s against up to 1500 px/s), so
  against a moving target it fell behind forever at a nearly constant
  bearing (hence "not rotating") and never got back inside its firing
  range. Its speed envelope is no longer hardcoded: it's derived from
  ships-vue's own `SPEED` constants and rescaled to `NPC_TICK_INTERVAL_MS`,
  so it cruises at 70% of a player's top speed with a player's
  acceleration (and a stronger brake). Black hole escapes use 100% of a
  player's top speed and triple acceleration.
- Enemy ships now keep fleeing a black hole until they're fully clear of
  its gravity, rather than turning back the instant they cross the fear
  radius. Without that hysteresis, a ship chasing a player who sits on a
  black hole just oscillated across the threshold, never escaping and
  never attacking.
- Tests added for aiming in every direction (including the frontend's
  quadrant-based bullet direction maths) and for closing on a fleeing
  player, so the speed model can't silently regress again.

Version 0.3.0 - 2026-09-05
------------------
- Black holes now affect NPC ships, not just players: an enemy Ship NPC is
  dragged towards any black hole within gravity range and is destroyed if
  it's swallowed, using the exact same maths ships-vue runs for the local
  player (`applyNpcImpacts`), rescaled from the client's 30fps fixed
  timestep to `NPC_TICK_INTERVAL_MS` so the pull is equally strong in
  wall-clock terms whatever tick rate this service runs at.
- Enemy ships now try to escape black holes: as soon as one comes within
  `enemyShipBlackHoleFear` (just inside gravity range) the ship abandons
  its target, turns its back on the black hole, burns full throttle away
  from it and holds its fire until it's clear. Escaping uses a dedicated,
  much punchier speed envelope (`enemyShipEscapeAccel`/
  `enemyShipEscapeMaxSpeed`) because the cruising values are slow enough
  that gravity would otherwise always win. A ship that's already deep in
  the well still can't get out - same as a player.
- Death by black hole is announced through the existing `playerDied` event
  with no killer, so nobody is credited with the kill and ships-vue plays
  the black hole implosion instead of the shot-down explosion.
- Added `npc_test.go`, covering the pull rate's parity with ships-vue,
  gravity range, being swallowed, escaping, and resuming the hunt after.

Version 0.2.4 - 2026-09-05
------------------
- Bugfix: the enemy ship missed nearly every shot, most visibly at close
  range with both ships stopped. Player positions on the wire are the
  ship's top-left anchor, so aiming straight at them put the aim point on
  the *corner* of the player's hitbox - a permanent grazing shot that the
  bullet's discrete ~37px-per-frame stepping usually skipped right past.
  ships-npc now resolves each target's ship size (indexing the same
  `GET /game/getShips` list ships-vue uses to resolve other players'
  ships) and aims at the ship's center, so shots have roughly half a ship
  of margin on every side.
- Chase distance/angle and nearest-target selection now also reason in ship
  centers rather than anchors, so standoff range is measured consistently.

Version 0.2.3 - 2026-09-05
------------------
- Bugfix: enemy ship bullets fired from its stored anchor position (its
  bounding box's top-left corner) instead of its visual center. Now fetches
  each public ship's `width`/`height` (falling back to `canvas.width`/
  `canvas.height`, mirroring ships-vue's `Player` constructor) and fires
  from `x + width/2, y + height/2` - the same raw, unscaled center point
  ships-vue's `Player.getCenteredPosition()` uses regardless of the ship's
  kill/death-based visual scale.

Version 0.2.2 - 2026-09-05
------------------
- Bugfix: bullets visually flew straight through the enemy Ship NPC on
  impact instead of disappearing. The bullet rendered on every screen
  (including the shooter's own) is a separate broadcast-fed copy from the
  one used for collision/damage bookkeeping, and is only ever cleared by an
  explicit `removeBullet` event - exactly like a hit player's client
  requests for player-vs-player hits. `handleNpcHit` now sends
  `removeBullet` for the spent bullet on every hit (damaging or lethal).

Version 0.2.1 - 2026-09-05
------------------
- Bugfix: enemy ship kept chasing/shooting after every player died, because
  ships-npc's minimal `PlayerData` never tracked `isDead` (self-reported by
  each client). `nearestPlayer` now skips dead players entirely.
- Bugfix: enemy ship moved at a constant, unchanging speed. It now
  accelerates while closing in on its target and brakes once inside
  shooting standoff range (see `enemyShipMaxSpeed`/`enemyShipAccel`/
  `enemyShipDecel` in `npc.go`).

Version 0.2.0 - 2026-09-05
------------------
- New destructible enemy Ship NPC (`type: "Ship"`): fetches public ships
  from ships-go's `GET /game/getShips` (with retry) and spawns one as a
  hostile entity that chases the nearest tracked player and shoots at it.
- Reuses the existing `newBullet`/`removeBullet` events for its own bullets
  (ships-npc owns their lifetime via a fixed timer, since no client "owns"
  foreign bullets).
- Handles new `npcHit` event (bullet-vs-npc hit reported by a client) to
  apply damage; on death, announces it via a `playerDied`-shaped message so
  the killer is credited through ships-go's existing kill-feed logic
  unmodified, then respawns the ship after a short delay.
- New `NPC_API_URL` env var (defaults to a URL derived from `NPC_WS_URL`).

Version 0.1.0 - 2026-09-04
------------------
- Initial implementation: NPC simulation extracted from `ships-go`.
- Connects to `ships-go`'s `/ws` endpoint as a websocket client, the same
  way a player would.
- Authenticates as an NPC controller via the `npcAuth` event
  (shared secret + localhost-only on the ships-go side).
- Tracks player positions from `gameBroadcast` messages (same incremental
  accumulation logic the game client uses) to decide where to spawn NPCs.
- Simulates black holes (spawn, growth/shrink, movement, expiry) and pushes
  the full current NPC batch to ships-go via the `npcUpdate` event on every
  tick, ready to support multiple simultaneous NPCs and NPC kinds.
