CHANGES
=======
Version 1.0.0 - 2026-09-XX
------------------
First release. All NPC simulation, previously embedded in `ships-go`, now
lives in this standalone service.

Nothing here is a bugfix: this is the initial version, so the list below
describes what the service does rather than how it got there.

- Connects to `ships-go`'s `/ws` endpoint as a websocket client, the same way
  a player would, and authenticates as the NPC controller via the `npcAuth`
  event (shared secret, localhost-only on the ships-go side). Only one
  controller may be connected at a time; if ships-go refuses this one, the
  reason is logged (almost always: a previous ships-npc is still running).
  The reconnect backoff resets after a connection that lasted a while, so one
  early hiccup no longer leaves ships-go without NPCs after every later drop.
- Tracks player positions from `gameBroadcast` messages, using the same
  incremental accumulation the game client does, so ships-go needs to expose
  no extra internal state.
- Simulates **black holes**: spawn, growth and shrink, movement and expiry,
  pushed to ships-go as the full current NPC batch on every tick. Any number
  of NPCs and NPC kinds travel in that one message, keyed by id.
- Simulates **enemy ships**: destructible hostiles that hunt players, drawn
  from the public ships at `GET /game/getShips`. Any number can run at once,
  each with its own target, throttle and fire cooldown, so they spread out
  and attack from different angles instead of behaving as one squadron.
- Enemy ships are *flown* the way a player flies one, not steered like a
  cursor: a ship picks the heading it wants, turns towards it at the same
  fixed rate a player gets from holding left/right, and always moves along
  the direction it is actually facing, accelerating while closing and braking
  inside standoff range. Guns fire along the nose, like a player's fixed
  forward guns, and a ship holds fire until it is genuinely lined up - the
  tolerance is derived per shot from how wide the target looks at its current
  distance. With nobody left to chase, ships coast to a stop.
- Aiming is done in ship centers, not the top-left anchors that travel on the
  wire, and uses the ship width/height each client reports. That matters
  because `GET /game/getShips` lists only the *public* ships: a player flying
  one of their own painting projects cannot be measured any other way, and
  guessing put the aim point hundreds of pixels off. Scale is honoured for
  the target's size (it does not move a ship's center, but it is the drawn
  size, which is what the victim's own client resolves hits against).
- Enemy ships collide with each other, and steer away from crowding rather
  than only un-overlapping after the fact - without that they converge on the
  same point and grind against each other permanently. Ship-vs-ship collision
  is resolved client-side in ships-vue, but a client can only move its own
  player, so NPC-against-NPC overlap is resolved here.
- Optionally, enemy ships **fight each other** (an admin toggle, off by
  default). They pick whichever is nearest - a player or a rival ship - so it
  adds a target rather than distracting them from players, and a kill reads
  in the feed like any other, naming the ship that scored it. This service
  flies its own bullets to make that possible: every other damage path is
  reported by a browser, but nobody is watching NPC against NPC, so those
  hits are resolved here, sweeping the whole path a bullet covered during the
  tick rather than testing where it ended up.
- Black holes are as lethal to an NPC as to a player. A ship is dragged in by
  gravity and destroyed if swallowed, using the exact maths ships-vue runs
  for the local player, rescaled from the client's fixed timestep to
  `NPC_TICK_INTERVAL_MS`. A ship abandons its target to escape one, using a
  punchier speed envelope, and keeps fleeing until fully clear rather than
  turning back the instant it crosses the fear radius - without that
  hysteresis a ship chasing a player sitting on a black hole just oscillates
  across the threshold. A ship already deep in the well cannot get out, the
  same as a player.
- Damage and death: a client reports its bullet hitting one of our ships with
  the `npcHit` event; this service applies the damage and sends `removeBullet`
  for the spent bullet, and on death announces it with a `playerDied`-shaped
  message so the killer is credited through ships-go's existing, unmodified
  kill-feed logic. Death by black hole is announced the same way with no
  killer, so nobody is credited and ships-vue plays the implosion instead of
  the shot-down explosion.
- Enemy ships keep their identity across deaths. A killed or despawned ship
  is retired and the next respawn revives it with the same id, name and hull,
  resetting only what a new life resets: position, heading, throttle and
  health. That is what makes them worth listing in ships-vue's scoreboard,
  and it reads like a recurring rival instead of an endless parade of
  strangers. Ships carry `kills`/`deaths` on the wire; deaths are counted
  here, kills are learned from ships-go's broadcast, and a kill whose victim
  is one of our own ships is ignored so a ship is never credited for its own
  death.
- Settings are controlled live from ships-vue's admin panel: enemy ship
  count, life, speed, fire rate and whether ships fight each other, plus the
  black hole cap and spawn period. ships-go owns the values - the browser
  cannot reach this service - and pushes them down the existing websocket as
  an `npcConfig` event, both when an admin saves and right after this service
  authenticates. No new port, endpoint or credential, and either process can
  restart without the two drifting apart. Changes take effect on the next
  tick: nothing restarts and no NPC respawns. Values are clamped on arrival
  (a zero fire rate would fire every tick). Lowering the ship count despawns
  the excess silently, with no `playerDied` - nobody killed them.
- Speeds are not hardcoded: the whole envelope is derived from ships-vue's own
  `SPEED` constants and rescaled to `NPC_TICK_INTERVAL_MS`, so the service is
  correct at any tick rate. Enemy ship speed is expressed in the game's own
  units, where `50` is a player at full throttle. Defaults: 1 enemy ship, 10
  life, speed 20, 500 ms fire rate, 2 black holes, 30 s spawn period - slower
  than a player by design, but shooting far more often.
- New `NPC_API_URL` env var, defaulting to a URL derived from `NPC_WS_URL`.
- Test suite covering the black hole pull's parity with ships-vue, gravity
  range and escape, aiming from every direction (including ships-vue's
  quadrant-based bullet maths), chasing, target selection, custom-ship
  geometry, bullet tunnelling, NPC-vs-NPC damage, ship separation, settings
  clamping and websocket write serialisation, plus a long-running fleet
  battle as a sanity check.
