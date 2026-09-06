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
	settings.NpcAttacksNpc = true
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
	for _, fleet := range s.retiredShips {
		for _, ship := range fleet {
			kills += ship.Kills
		}
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

// The admin panel allows 100 ships per fleet, so 200 is the worst case a
// deployment can be asked for. This measures the real cost of a tick at
// that size and fails if it stops fitting comfortably inside the tick
// interval: the simulation is single-threaded, so a tick that overruns
// makes every NPC on every screen stutter.
func TestFullFleetsFitInsideATick(t *testing.T) {
	if testing.Short() {
		t.Skip("performance measurement")
	}

	const (
		perFleet     = 100
		ticks        = 600
		tickInterval = 100 * time.Millisecond
	)

	f := &fakeSender{}
	settings := defaultNpcSettings()
	settings.EnemyShipController = controllerBoth
	settings.EnemyShips = perFleet
	settings.AiShips = perFleet
	settings.MaxBlackHoles = 10
	// Everything hunting everything: the most expensive matrix there is,
	// since every ship considers every other ship as a target and every
	// bullet is tested against every ship.
	settings.NpcAttacksPlayers = true
	settings.NpcAttacksNpc = true
	settings.NpcAttacksAi = true
	settings.AiAttacksPlayers = true
	settings.AiAttacksNpc = true
	settings.AiAttacksAi = true
	s := settingsSim(f, settings)
	if s.policy == nil {
		t.Fatal("no AI policy embedded; the AI fleet would fall back to the rule controller")
	}

	players := map[string]PlayerData{}
	for i := 0; i < 4; i++ {
		id := string(rune('a' + i))
		players[id] = PlayerData{SocketId: id, ShipId: "s1", X: float32(i * 700), Y: float32(i * 500),
			Width: 100, Height: 200, Scale: 1}
	}

	now := time.Now()
	started := time.Now()
	for i := 0; i < ticks; i++ {
		now = now.Add(tickInterval)
		s.mu.Lock()
		s.tickEnemyShips(players, now)
		s.advanceBullets(now)
		s.mu.Unlock()
	}
	elapsed := time.Since(started)
	perTick := elapsed / ticks

	ruleShips, aiShips := s.fleetCount(controllerRule), s.fleetCount(controllerAi)
	t.Logf("%d ships (%d rule / %d AI), %d bullets in flight: %v per tick (%.1f%% of a %v tick)",
		len(s.enemyShips), ruleShips, aiShips, len(s.activeBullets),
		perTick, 100*float64(perTick)/float64(tickInterval), tickInterval)

	// Not exactly perFleet: a death arms a respawn delay, so at any instant
	// a few slots are legitimately waiting to be refilled. What matters is
	// that the fleets are being kept near strength rather than quietly
	// dwindling, which is what would make the timing above meaningless.
	if ruleShips < perFleet-5 || aiShips < perFleet-5 {
		t.Fatalf("fleets dwindled: %d rule, %d AI, want about %d each", ruleShips, aiShips, perFleet)
	}
	if ruleShips > perFleet || aiShips > perFleet {
		t.Fatalf("a fleet grew past its cap: %d rule, %d AI", ruleShips, aiShips)
	}
	if perTick > tickInterval/2 {
		t.Fatalf("a tick costs %v, more than half the %v budget", perTick, tickInterval)
	}
}

// With the two fleets set against each other on identical physics, both
// have to be able to score: an AI fleet that never lands a shot would mean
// the policy is not really flying, and the admin toggle would be a lie.
func TestBothFleetsCanFight(t *testing.T) {
	f := &fakeSender{}
	settings := defaultNpcSettings()
	settings.EnemyShipController = controllerBoth
	settings.EnemyShips = 10
	settings.AiShips = 10
	settings.MaxBlackHoles = 0
	settings.NpcAttacksPlayers = false
	settings.AiAttacksPlayers = false
	settings.NpcAttacksAi = true
	settings.AiAttacksNpc = true
	s := settingsSim(f, settings)
	if s.policy == nil {
		t.Skip("no AI policy embedded")
	}

	players := map[string]PlayerData{"p1": {SocketId: "p1", ShipId: "s1", X: 0, Y: 0,
		Width: 100, Height: 200, Scale: 1}}

	now := time.Now()
	for i := 0; i < 4000; i++ {
		now = now.Add(100 * time.Millisecond)
		s.mu.Lock()
		s.tickEnemyShips(players, now)
		s.advanceBullets(now)
		s.mu.Unlock()
	}

	kills := map[controllerKind]int{}
	count := func(ship *enemyShipState) { kills[ship.controller] += ship.Kills }
	for _, ship := range s.enemyShips {
		count(ship)
	}
	for _, fleet := range s.retiredShips {
		for _, ship := range fleet {
			count(ship)
		}
	}

	t.Logf("head to head: rule %d kills, AI %d kills", kills[controllerRule], kills[controllerAi])
	if kills[controllerRule] == 0 {
		t.Fatal("the rule fleet never scored")
	}
	if kills[controllerAi] == 0 {
		t.Fatal("the AI fleet never scored: the policy is not really flying these ships")
	}
}
