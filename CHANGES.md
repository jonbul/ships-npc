CHANGES
=======
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
