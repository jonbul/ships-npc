package main

import "sync"

// playerTracker accumulates player positions from ships-go's incremental
// gameBroadcast messages (which only include players that moved since the
// last tick), the same way the game client does. This lets ships-npc know
// the full set of currently active players and their latest known position
// without ships-go having to expose any extra internal state.
type playerTracker struct {
	mu      sync.RWMutex
	players map[string]PlayerData
}

func newPlayerTracker() *playerTracker {
	return &playerTracker{players: make(map[string]PlayerData)}
}

func (t *playerTracker) apply(msg gameBroadcast) {
	t.mu.Lock()
	defer t.mu.Unlock()

	for id, p := range msg.Players {
		t.players[id] = p
	}

	active := make(map[string]struct{}, len(msg.ActivePlayerIds))
	for _, id := range msg.ActivePlayerIds {
		active[id] = struct{}{}
	}
	for id := range t.players {
		if _, ok := active[id]; !ok {
			delete(t.players, id)
		}
	}
}

// snapshot returns a copy of the currently known players, safe to iterate
// without holding the tracker's lock.
func (t *playerTracker) snapshot() map[string]PlayerData {
	t.mu.RLock()
	defer t.mu.RUnlock()

	out := make(map[string]PlayerData, len(t.players))
	for id, p := range t.players {
		out[id] = p
	}
	return out
}

func (t *playerTracker) count() int {
	t.mu.RLock()
	defer t.mu.RUnlock()
	return len(t.players)
}
