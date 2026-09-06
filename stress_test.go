package main

import (
	"math"
	"testing"
	"time"
)

// A full fleet fighting each other, over a long run: the point is that the
// simulation stays sane - ships die and respawn, nobody stacks, the bullet
// map doesn't grow without bound, and no ship kills itself.
func TestFleetFightingItselfStaysSane(t *testing.T) {
	f := &fakeSender{}
	settings := defaultNpcSettings()
	settings.EnemyShips = 8
	settings.EnemyShipsFightEachOther = true
	settings.MaxBlackHoles = 0
	s := settingsSim(f, settings)

	players := map[string]PlayerData{"p1": {SocketId: "p1", ShipId: "s1", X: 50000, Y: 50000}}

	// Driven on a synthetic clock rather than through tick(), which reads
	// the wall clock: a few thousand ticks run in milliseconds of real
	// time, so every fire-rate cooldown would still be counting down and
	// almost nothing would ever shoot.
	now := time.Now()
	for i := 0; i < 3000; i++ {
		now = now.Add(100 * time.Millisecond)
		s.mu.Lock()
		s.tickEnemyShips(players, now)
		s.advanceBullets(now)
		s.mu.Unlock()
	}

	if len(f.died) == 0 {
		t.Fatal("with friendly fire on and a whole fleet, ships should be killing each other")
	}
	for _, died := range f.died {
		if died.From == died.PlayerId {
			t.Fatalf("a ship killed itself: %+v", died)
		}
		if died.From == "" {
			t.Fatalf("kill feed would show an anonymous killer: %+v", died)
		}
	}
	if got := len(s.enemyShips); got > settings.EnemyShips {
		t.Fatalf("fleet grew past its cap: %d", got)
	}
	// Bullets are retired on hit or after their lifetime; anything else
	// means the tracking map leaks for the life of the process.
	if got := len(s.activeBullets); got > settings.EnemyShips*40 {
		t.Fatalf("in-flight bullet map looks unbounded: %d", got)
	}

	kills := 0
	for _, ship := range s.enemyShips {
		kills += ship.Kills
	}
	for _, ship := range s.retiredShips {
		kills += ship.Kills
	}
	if kills == 0 {
		t.Fatal("no NPC was ever credited with a kill")
	}

	ships := make([]*enemyShipState, 0, len(s.enemyShips))
	for _, ship := range s.enemyShips {
		ships = append(ships, ship)
	}
	for i := range ships {
		for j := i + 1; j < len(ships); j++ {
			a, b := ships[i], ships[j]
			aw, ah := a.ship.size()
			bw, bh := b.ship.size()
			ox := math.Min(a.X+aw, b.X+bw) - math.Max(a.X, b.X)
			oy := math.Min(a.Y+ah, b.Y+bh) - math.Max(a.Y, b.Y)
			if ox > 0 && oy > 0 {
				t.Fatalf("ships %s and %s overlap by (%v, %v) while fighting", a.Id, b.Id, ox, oy)
			}
		}
	}
}
