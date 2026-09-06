package main

import (
	"math"
	"testing"
	"time"
)

// ramSim seeds two enemy ships sitting exactly on top of each other, which
// is the only thing the ramming pass looks at.
func ramSim(f *fakeSender, mutate func(*npcSettings)) (*npcSimulator, *enemyShipState, *enemyShipState) {
	settings := defaultNpcSettings()
	settings.EnemyShipController = string(controllerNone)
	settings.EnemyShips = 0
	settings.AiShips = 0
	settings.MaxBlackHoles = 0
	if mutate != nil {
		mutate(&settings)
	}
	s := settingsSim(f, settings)

	ship := publicShip{Id: "s1", Name: "T", Width: 100, Height: 200}
	mk := func(id string, controller controllerKind, x, y float64) *enemyShipState {
		state := &enemyShipState{ship: ship, controller: controller, spawnedAt: time.Now(), NpcData: NpcData{
			Type: NpcTypes.Ship, Id: id, ShipId: "s1", Scale: 1, X: x, Y: y, Life: 20, MaxLife: 20,
		}}
		s.enemyShips[id] = state
		return state
	}
	// Overlapping by half a hull: touching, but not so exactly aligned that
	// the separation pass has no axis to choose.
	return s, mk("a", controllerRule, 0, 0), mk("b", controllerAi, 40, 40)
}

// A player who rams an NPC takes damage, so the NPC has to take it too, and
// two NPCs must be no safer with each other. Anything else hands the NPCs an
// advantage no player has.
func TestTouchingShipsDamageEachOther(t *testing.T) {
	s, a, b := ramSim(&fakeSender{}, nil)

	s.ramEnemyShips(time.Now())

	if a.Life != 18 || b.Life != 18 {
		t.Fatalf("both ships should have taken %v ram damage: got a=%v b=%v",
			float32(enemyShipRamDamage), a.Life, b.Life)
	}
}

// The contact is mutual and belongs to both fleets equally: it must not
// matter which brain is flying, nor whether the attack matrix lets these two
// shoot each other. A collision is not an attack.
func TestRamDamageIgnoresTheAttackMatrix(t *testing.T) {
	s, a, b := ramSim(&fakeSender{}, func(settings *npcSettings) {
		settings.NpcAttacksNpc = false
		settings.NpcAttacksAi = false
		settings.AiAttacksNpc = false
		settings.AiAttacksAi = false
	})

	s.ramEnemyShips(time.Now())

	if a.Life == 20 || b.Life == 20 {
		t.Fatalf("a collision should hurt regardless of who may shoot whom: a=%v b=%v", a.Life, b.Life)
	}
}

// Ships grinding along each other must be rate-limited exactly as a player's
// browser rate-limits itself, or an NPC pinned against another would be
// drained in a handful of ticks while a player in the same spot survives.
func TestRamDamageIsRateLimitedPerPair(t *testing.T) {
	s, a, b := ramSim(&fakeSender{}, nil)
	now := time.Now()

	for i := 0; i < 20; i++ {
		s.ramEnemyShips(now)
		now = now.Add(10 * time.Millisecond)
	}
	if a.Life != 18 {
		t.Fatalf("repeated contact inside the cooldown should hit once: got %v", a.Life)
	}

	s.ramEnemyShips(now.Add(enemyShipRamCooldown))
	if a.Life != 16 {
		t.Fatalf("contact should hurt again after the cooldown: got %v", a.Life)
	}
	if b.Life != a.Life {
		t.Fatalf("both sides of a collision take the same damage: a=%v b=%v", a.Life, b.Life)
	}
}

// The cooldown map is keyed per pair, so with two full fleets it would grow
// towards 20k entries if nothing ever removed them.
func TestRamCooldownsArePruned(t *testing.T) {
	s, _, _ := ramSim(&fakeSender{}, nil)
	now := time.Now()

	s.ramEnemyShips(now)
	if len(s.ramHitAt) != 1 {
		t.Fatalf("the touching pair should be on cooldown: got %d entries", len(s.ramHitAt))
	}
	// Far enough ahead that the entry has expired, with the ships moved
	// apart so it isn't simply re-armed.
	s.enemyShips["b"].X = 100000
	s.ramEnemyShips(now.Add(10 * enemyShipRamCooldown))
	if len(s.ramHitAt) != 0 {
		t.Fatalf("expired cooldowns should be dropped: got %d entries", len(s.ramHitAt))
	}
}

func TestContactDamageCanBeSwitchedOff(t *testing.T) {
	s, a, b := ramSim(&fakeSender{}, func(settings *npcSettings) {
		settings.ContactDamage = false
	})

	s.ramEnemyShips(time.Now())
	if a.Life != 20 || b.Life != 20 {
		t.Fatalf("no damage expected with contact damage off: a=%v b=%v", a.Life, b.Life)
	}

	// Ships must still stop overlapping: the push is physics, not damage.
	s.separateEnemyShips()
	if shipsOverlap(a, b) {
		t.Fatal("ships should still be pushed apart with contact damage off")
	}
}

// A player's browser reports ramming an NPC as an npcHit with no bullet, the
// same convention playerHit already uses for damage with nothing to remove
// from anyone's screen.
func TestPlayerRamDamagesTheNpc(t *testing.T) {
	f := &fakeSender{}
	s, a, _ := ramSim(f, nil)

	s.handleNpcHit(npcHitMsg{NpcId: a.Id, BulletId: "", From: "p1", BulletCharge: 2})

	if a.Life != 18 {
		t.Fatalf("a player's ram should damage the NPC: got %v", a.Life)
	}
}

func TestPlayerRamIsIgnoredWithContactDamageOff(t *testing.T) {
	f := &fakeSender{}
	s, a, _ := ramSim(f, func(settings *npcSettings) { settings.ContactDamage = false })

	s.handleNpcHit(npcHitMsg{NpcId: a.Id, BulletId: "", From: "p1", BulletCharge: 2})

	if a.Life != 20 {
		t.Fatalf("ram reports should be ignored with contact damage off: got %v", a.Life)
	}
}

// Switching contact damage off must not disarm the shooting: a bullet is a
// different damage path entirely.
func TestBulletHitsStillLandWithContactDamageOff(t *testing.T) {
	f := &fakeSender{}
	s, a, _ := ramSim(f, func(settings *npcSettings) { settings.ContactDamage = false })

	s.handleNpcHit(npcHitMsg{NpcId: a.Id, BulletId: "b1", From: "p1", BulletCharge: 5})

	if a.Life != 15 {
		t.Fatalf("bullet damage should be unaffected: got %v", a.Life)
	}
}

// A ram that kills has to be announced like any other death, or the ship
// stays on screen at zero life and is never revived.
func TestRamKillIsAnnounced(t *testing.T) {
	f := &fakeSender{}
	s, a, b := ramSim(f, nil)
	a.Life = 1
	b.Life = 1

	s.ramEnemyShips(time.Now())

	if len(f.died) != 2 {
		t.Fatalf("both ships should have been announced dead: got %d", len(f.died))
	}
	// Each is credited to the other, exactly as two players colliding would
	// be from their own browsers.
	credited := map[string]string{}
	for _, died := range f.died {
		credited[died.PlayerId] = died.From
	}
	if credited["a"] != "b" || credited["b"] != "a" {
		t.Fatalf("each ship should be credited to the one it hit: %v", credited)
	}
}

// A hundred ships per fleet used to materialise in a heap: the spawn box was
// a fixed 3000x3000 around the players however many ships went into it, and
// uniform random points clump even when there is room. Both halves are
// checked here, because fixing only one of them still leaves a pile.
func TestFleetsSpawnSpreadOut(t *testing.T) {
	f := &fakeSender{}
	settings := defaultNpcSettings()
	settings.EnemyShipController = string(controllerBoth)
	settings.EnemyShips = 100
	settings.AiShips = 100
	settings.MaxBlackHoles = 0
	s := settingsSim(f, settings)

	s.tickEnemyShips(somePlayers(), time.Now())

	ships := s.sortedEnemyShips()
	if len(ships) != 200 {
		t.Fatalf("expected both fleets in full: got %d", len(ships))
	}

	// Nothing may spawn inside another ship, and the typical gap has to be
	// wide enough that they read as a scattered fleet rather than a blob.
	tooClose := 0
	for i := range ships {
		nearest := math.Inf(1)
		for j := range ships {
			if i == j {
				continue
			}
			nearest = math.Min(nearest, math.Hypot(ships[i].X-ships[j].X, ships[i].Y-ships[j].Y))
		}
		if nearest < 200 {
			tooClose++
		}
		if shipsOverlap(ships[i], ships[(i+1)%len(ships)]) {
			t.Fatalf("ships %s and %s spawned overlapping", ships[i].Id, ships[(i+1)%len(ships)].Id)
		}
	}
	// Not zero: rejection sampling is best-effort by design, and a handful
	// of close pairs is fine. A pile is not.
	if tooClose > len(ships)/10 {
		t.Fatalf("%d of %d ships spawned within 200px of another; fleet is bunched up", tooClose, len(ships))
	}
}

// The box has to grow with the fleet. A margin that comfortably holds one
// ship is standing room only for two hundred, and then every spawn falls
// back to best-of-N inside a box that cannot possibly satisfy the spacing.
func TestSpawnAreaGrowsWithTheFleet(t *testing.T) {
	f := &fakeSender{}
	settings := defaultNpcSettings()
	settings.MaxBlackHoles = 0
	settings.EnemyShips = 1
	settings.AiShips = 0
	small := settingsSim(f, settings).spawnMargin()

	settings.EnemyShipController = string(controllerBoth)
	settings.EnemyShips = 100
	settings.AiShips = 100
	large := settingsSim(f, settings).spawnMargin()

	if small != enemyShipSpawnMargin {
		t.Fatalf("a single ship should use the floor: got %v want %v", small, float32(enemyShipSpawnMargin))
	}
	if large <= small*2 {
		t.Fatalf("200 ships should get a far bigger box: got %v with %v for one ship", large, small)
	}
}

// The defaults here and in ships-go must agree: they are what runs before an
// admin opens the panel, and the two processes never reconcile them.
func TestDefaultGameRules(t *testing.T) {
	settings := defaultNpcSettings()
	if !settings.ContactDamage {
		t.Error("ramming should hurt by default")
	}
	if settings.KillScaling {
		t.Error("ships growing with their score should be off by default")
	}
}
