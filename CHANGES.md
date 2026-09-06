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
  the direction it is actually facing. Guns fire along the nose, like a
  player's fixed forward guns, and a ship holds fire until it is genuinely
  lined up - the tolerance is derived per shot from how wide the target looks
  at its current distance. With nobody left to chase, ships coast to a stop.
- Ships fight by making **attack runs** rather than parking in front of their
  target: they close with the nose on it and shoot, then break away, weaving,
  and come round for another pass. A ship never brakes to a standstill and
  never flies a straight line at anything, so it is not the free target a
  hovering one is. The cost is deliberate - the guns fire along the heading,
  so a ship can only shoot while pointing at its target, and a ship pointing
  steadily at something is a ship flying a predictable course. A parked
  turret would land far more shots and die to the first player who noticed
  it. Each ship's phase lengths and weave are jittered, so a fleet does not
  swing back and forth in formation.
- Ships **get out of the way of bullets**. Incoming fire relayed by ships-go
  is flown forward each tick, and a ship that finds one on a collision course
  inside its lookahead abandons what it was doing and turns *across* the
  bullet's path - never away from it, since a bullet travels several times a
  ship's top speed and running only delays the hit. The dodge is committed to
  for a moment rather than reconsidered every tick: a manoeuvre that is
  re-decided constantly is no manoeuvre at all.
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
- Ramming hurts NPCs exactly as much as it hurts players, on the same damage
  and the same per-pair cooldown ships-vue uses. A player's browser reports
  the ship it rammed with an `npcHit` carrying no bullet; two NPCs colliding
  is settled here, since no browser is watching. A collision is not an
  attack, so it ignores the attack matrix entirely - fleets forbidden to
  shoot each other still get hurt running into each other. Without all of
  this an NPC could be rammed for free, which is an advantage no player has.
- Contact damage can be turned off from the admin panel, for everyone at
  once. Ships are still pushed apart; only the damage stops.
- Black hole duration is an admin setting. It is read every tick rather than
  stamped on a black hole when it spawns, so shortening it retires the ones
  already on the map instead of applying only to the next one.
- Black holes now spawn for a lone player, and open when they are supposed
  to. Two things kept them off the map: they were gated on there being more
  than one player, so anybody testing the fleet alone never saw a single one
  (enemy ships have always spawned for a single player, and a black hole is
  a hazard for them too); and their size was stepped up 0.01 per tick, so
  how fast one opened depended on the tick rate and had nothing to do with
  its duration - at this service's 100 ms tick it needed 10 s just to become
  visible, which a 15 s black hole spent almost its whole life doing. Size
  is now derived from elapsed time: it opens over 3 s (or a third of its
  duration, whichever is shorter), stays full, and collapses so that it is
  gone exactly when its duration is up rather than lingering for a
  tick-counted collapse on top of it.
- Ship life is read from `shipLife`, one setting shared with the players
  rather than the NPC-only `enemyShipLife` it used to be. Same value, same
  meaning on both sides: a new ship spawns with it, an existing one keeps
  what it spawned with.
- Ships are normalised to the admin-configured standard size (100 by
  default), the same rule ships-vue applies to every hull it draws. This is
  not cosmetic here: the scale is the collision box, and ships were flown at
  scale 1, so a ship built on a 400px canvas was rammed, crowded and shot at
  from four times the distance a player could see it. A change from the
  panel re-scales the ships already flying, not just the next ones to spawn.
- The spawn box grows with the fleet, and spawn points keep their distance
  from ships and players already on the map. It used to be a fixed 3000x3000
  around the players however many ships went into it, so a 100 v 100 test
  materialised as a heap - and even with room, uniformly random points clump
  into clusters rather than spreading out. Ships still converge on the
  players within seconds; they just no longer start on top of each other.
- Two independent fleets, each flown by a different brain and told apart by
  its name tag: `[NPC]` ships fly the hand-written rules, `[AI]` ships fly a
  small neural network. The admin panel chooses which fleets run - neither,
  either, or both - and sizes each one separately, up to 100 ships per fleet.
- The **AI controller** is a 16-32-32-3 multilayer perceptron, about 1.5k
  parameters, evaluated in plain Go with no framework, no cgo and no network
  call. Its weights live in `aiPolicy.json`, compiled into the binary with
  `go:embed`; if they are missing or malformed the ship falls back to the
  rules rather than failing to start. It is trained by behaviour cloning from
  the rule controller (`SHIPS_NPC_TRAIN=1 go test -run TestTrainAiPolicy`),
  which needs no reward design and converges in about 20 seconds. Angles are
  fed as sin/cos pairs, never as raw radians, because +179 deg and -179 deg
  are nearly the same heading but the two furthest-apart numbers. The network
  is shown the incoming bullet and where the ship is in its attack run as
  well as the geometry: a brain that could not see the threat would fly
  straight into fire, and one that could not see the phase would have to
  average the two halves of the manoeuvre together and fly neither. The
  attack phase is therefore advanced by the simulator for every ship,
  whatever brain flies it, rather than inside the rule controller.
- Both brains only ever return a *command* - turn, thrust, fire - which is
  then applied by the same shared code that already enforced the envelope. An
  AI ship therefore cannot out-turn, out-accelerate or out-shoot a rule ship
  or a player, whatever the network outputs; that is asserted by tests rather
  than assumed.
- Who may shoot whom is a full matrix, not a single toggle: each of the two
  fleets can be allowed or forbidden to attack players, `[NPC]` ships and
  `[AI]` ships, in any combination. Targeting and bullet damage both consult
  it, so a forbidden pair is neither hunted nor hurt. A ship picks whichever
  permitted target is nearest, so enabling a pair adds a target rather than
  distracting it from players, and a kill reads in the feed like any other,
  naming the ship that scored it. This service flies its own bullets to make
  that possible: every other damage path is reported by a browser, but nobody
  is watching NPC against NPC, so those hits are resolved here, sweeping the
  whole path a bullet covered during the tick rather than testing where it
  ended up.
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
  strangers. Each dead ship comes back on its own clock, a few seconds after
  it died plus a little jitter, so a fleet trickles back in the order it was
  destroyed instead of a single shared timer bringing the whole thing back on
  one tick. Ships carry `kills`/`deaths` on the wire; deaths are counted
  here, kills are learned from ships-go's broadcast, and a kill whose victim
  is one of our own ships is ignored so a ship is never credited for its own
  death.
- Settings are controlled live from ships-vue's admin panel: which fleets
  run, the size of each, the attack matrix, life, speed, fire rate, plus the
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
