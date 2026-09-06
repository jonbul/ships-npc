CHANGES
=======
Version 0.7.2 - 2026-09-06
------------------
- Bugfix: NPC ships aimed hundreds of pixels away from players flying a ship
  of their own making, so they turned towards a point beside their target and
  shot past it. A player's position on the wire is their ship's top-left
  anchor, and the middle was worked out from the ship's size looked up by
  shipId - but GET /game/getShips lists only the *public* ships, so a
  player's own painting project could not be measured at all. A live 1000x1000
  custom ship was being aimed at as if it were the 100x100 fallback: the aim
  point was ~600px out, and the aim tolerance (the half-angle the target
  subtends, which decides when it is worth firing) was several times too
  generous, so shots were loosed while still pointing well past the player.
- The client now sends its ship's real width/height, so the geometry is known
  exactly instead of guessed: no lookup, and correct for any custom ship. The
  ship-list lookup remains as a fallback. Scale is honoured too - it does not
  move a ship's center, but it is the drawn size, which is what the victim's
  own client resolves our hits against.

Version 0.7.1 - 2026-09-06
------------------
- Bugfix: NPC ships aimed at the wrong point, and so turned the wrong way,
  against any player flying a ship of their own making. Target positions
  arrive as the ship's top-left anchor and are converted to a center using
  the ship's size, looked up in GET /game/getShips - but that endpoint only
  lists the *public* ships, so a player's own painting project isn't there.
  The lookup miss fell back to a zero offset, i.e. aiming at the corner:
  measured at 26-35 degrees of steady aiming error, enough that the ship
  chased and shot past its target indefinitely. It now resolves an unknown
  shipId to the same fallback ship ships-vue draws that player with, so the
  NPC aims at the ship everyone can actually see.

Version 0.7.0 - 2026-09-06
------------------
- Enemy ships can now fight each other, controlled by a new toggle in the
  admin panel (off by default). They pick whichever is nearest - a player or
  a rival ship - so it adds a target rather than distracting them from
  players, and a kill reads in the feed like any other, naming the ship that
  scored it.
- To make that possible this service now flies its own bullets rather than
  only timing them out. Every other damage path is reported by a browser -
  a player's client tells us when its bullet hit one of our ships, and the
  victim's client handles our bullets hitting a player - but nobody is
  watching NPC against NPC, so those hits are resolved here. Hit detection
  sweeps the whole path a bullet covered during the tick instead of testing
  where it ended up: NPC_TICK_INTERVAL_MS is configurable and a bullet can
  easily cross more than a ship's width in one step.
- A ship never shoots itself (its bullets start inside its own hull) and
  stops avoiding the rival it is currently hunting, since the separation
  distance is wider than the range it wants to fight from - without that,
  two rivals just orbit each other and never line up a shot.
- Bugfix: a pile of ships could be left a few pixels overlapped. Separation
  resolves one pair at a time, so with more than two ships a single pass can
  push one straight back into a pair it had already separated. It now
  repeats until nothing moves. Reproduced in about 2% of runs before, none
  after.

Version 0.6.1 - 2026-09-06
------------------
- Bugfix: two goroutines wrote to the websocket at the same time. The tick
  loop sends the NPC snapshot while the reading goroutine sends bullet
  removals and player deaths, and a websocket allows exactly one writer -
  concurrent writes interleave into a corrupt frame that ships-go cannot
  decode. Writes are now serialised, carry a ten second deadline (a blocking
  write would freeze every NPC, because sends happen while the simulator is
  locked), and a failed write closes the connection so the usual reconnect
  runs.
- Bugfix: a newly spawned enemy ship was invisible and impossible to hit
  until it happened to turn. Its heading was exactly zero at spawn and the
  field was omitted from the wire when zero, so the browser had nothing to
  assign and poisoned the ship's position with NaN. The heading is now always
  sent, and ships spawn pointing in a random direction.

Version 0.6.0 - 2026-09-05
------------------
- Enemy ships keep their identity when they die. A destroyed ship used to be
  replaced by a brand new one with a fresh uuid, a random hull and a random
  name, so no NPC ever built up a record. A killed (or despawned) ship is now
  retired and the next respawn revives it with the same id, name and hull,
  resetting only what a new life resets: position, heading, throttle and
  health. This is what makes NPCs worth listing in ships-vue's scoreboard,
  and it reads like a recurring rival instead of an endless parade of
  strangers.
- Enemy ships now carry `kills`/`deaths` on the wire. Deaths are counted
  here; kills are learned from the `kills` list in ships-go's gameBroadcast,
  because the hit is always detected by the victim's own client and never by
  this service. A kill event whose victim is one of our own ships is ignored,
  so a ship is never credited for its own death.
- ships-go now refuses a second NPC controller. When that happens this
  service logs why (almost always: a previous ships-npc is still running)
  instead of silently retrying.
- The reconnect backoff resets after a connection that lasted a while, so one
  early hiccup no longer leaves ships-go without NPCs for 30s after every
  later drop.

Version 0.5.0 - 2026-09-05
------------------
- Enemy ships are now *flown* like a player flies one instead of being
  steered like a cursor. Previously a ship snapped its heading straight to
  the bearing of its target and moved along that bearing, so it pivoted and
  changed direction instantly - obviously non-human. Now it picks the
  heading it wants, turns towards it at the same fixed rate a player gets
  from holding left/right (ships-vue's `SPEED.ROTATION`, taking the short
  way round), and always moves along the direction it is actually facing.
  Combined with the existing progressive acceleration/braking, a ship now
  banks into turns and overshoots like a real ship.
- Guns fire along the ship's nose, like a player's fixed forward guns,
  instead of along the bearing to the target - which used to send bullets
  out at a visible angle to the ship whenever it hadn't finished turning.
  A ship now holds fire until it's genuinely lined up; the tolerance is
  derived per shot from how wide the target looks at its current distance,
  so close targets are easy and distant ones demand a tighter line-up.
- Enemy ships now collide with **each other**. Ship-vs-ship collision is
  resolved client-side in ships-vue, but a client can only ever move its
  own player, so nothing was resolving NPC-against-NPC overlap and a fleet
  hunting the same player collapsed into a single pile. This service owns
  every ship, so it resolves them here, mirroring the client's axis-of-
  least-penetration push and moving each ship half the overlap so the
  result is symmetric.
- Ships also *steer* away from crowding, not just un-overlap after the
  fact: without that they converge on the same point and grind against
  each other permanently, with collision only ever undoing the last step.
  A group now fans out into a loose formation and attacks from several
  angles.
- With nobody left to chase, ships coast to a stop instead of holding
  their last speed forever.

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
