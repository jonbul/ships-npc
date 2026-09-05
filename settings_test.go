package main

import (
	"testing"
	"time"
)

func settingsSim(f *fakeSender, settings npcSettings) *npcSimulator {
	ship := publicShip{Id: "s1", Name: "T", Width: 100, Height: 200}
	s := newNpcSimulator(f, []publicShip{ship}, 100*time.Millisecond)
	s.applySettings(settings)
	return s
}

func somePlayers() map[string]PlayerData {
	return map[string]PlayerData{"p1": {SocketId: "p1", ShipId: "s1", X: 0, Y: 0}}
}

func TestFleetGrowsToConfiguredSize(t *testing.T) {
	f := &fakeSender{}
	settings := defaultNpcSettings()
	settings.EnemyShips = 4
	s := settingsSim(f, settings)

	s.tickEnemyShips(somePlayers(), time.Now())

	if len(s.enemyShips) != 4 {
		t.Fatalf("expected 4 enemy ships, got %d", len(s.enemyShips))
	}
	if len(s.snapshot()) != 4 {
		t.Fatalf("all ships should be in the broadcast batch, got %d", len(s.snapshot()))
	}
}

func TestLoweringTheCountDespawnsSilently(t *testing.T) {
	f := &fakeSender{}
	settings := defaultNpcSettings()
	settings.EnemyShips = 5
	s := settingsSim(f, settings)
	s.tickEnemyShips(somePlayers(), time.Now())

	settings.EnemyShips = 2
	s.applySettings(settings)
	s.tickEnemyShips(somePlayers(), time.Now())

	if len(s.enemyShips) != 2 {
		t.Fatalf("expected 2 enemy ships after lowering the count, got %d", len(s.enemyShips))
	}
	// Nobody killed them, so there must be no explosion/kill-feed entry.
	if len(f.died) != 0 {
		t.Fatalf("despawning must not announce deaths, got %d", len(f.died))
	}
}

func TestDisablingEnemyShipsRemovesThemAll(t *testing.T) {
	f := &fakeSender{}
	settings := defaultNpcSettings()
	settings.EnemyShips = 3
	s := settingsSim(f, settings)
	s.tickEnemyShips(somePlayers(), time.Now())

	settings.EnemyShips = 0
	s.applySettings(settings)
	s.tickEnemyShips(somePlayers(), time.Now())

	if len(s.enemyShips) != 0 {
		t.Fatalf("expected no enemy ships, got %d", len(s.enemyShips))
	}
}

func TestSpeedChangesTopSpeedLive(t *testing.T) {
	f := &fakeSender{}
	settings := defaultNpcSettings()
	settings.EnemyShipSpeed = 25
	s := settingsSim(f, settings)
	slow := s.maxSpeed

	settings.EnemyShipSpeed = clientSpeedMax
	s.applySettings(settings)

	if s.maxSpeed <= slow {
		t.Fatalf("raising the speed must raise max speed: %v -> %v", slow, s.maxSpeed)
	}
	// The setting is in the game's own units, so at SPEED.MAX the ship
	// must be exactly as fast as a player at full throttle.
	if want := clientSpeedMax * s.frameScale; s.maxSpeed != want {
		t.Fatalf("max speed should equal a player's top speed: got %v want %v", s.maxSpeed, want)
	}
}

func TestDefaultSpeedIsSlowerThanAPlayer(t *testing.T) {
	// A player at full throttle must always be able to outrun the default
	// enemy ship, otherwise there's no escaping one.
	if defaultEnemyShipSpeed >= clientSpeedMax {
		t.Fatalf("default enemy ship speed %v must be below a player's top speed %v",
			defaultEnemyShipSpeed, clientSpeedMax)
	}
}

func TestFireRateIsRespectedPerShip(t *testing.T) {
	f := &fakeSender{}
	settings := defaultNpcSettings()
	settings.EnemyShips = 2
	settings.EnemyShipFireRateMs = 1000
	s := settingsSim(f, settings)

	players := somePlayers()
	now := time.Now()
	s.tickEnemyShips(players, now)
	// Park both ships in firing range of the player at (0,0).
	for _, ship := range s.enemyShips {
		ship.X, ship.Y = 200, 200
	}

	s.tickEnemyShips(players, now)
	if len(f.bullets) != 2 {
		t.Fatalf("each ship should fire once, got %d shots", len(f.bullets))
	}

	// Still inside the cooldown: nobody may fire again.
	s.tickEnemyShips(players, now.Add(500*time.Millisecond))
	if len(f.bullets) != 2 {
		t.Fatalf("no ship may fire again within the cooldown, got %d shots", len(f.bullets))
	}

	s.tickEnemyShips(players, now.Add(1500*time.Millisecond))
	if len(f.bullets) != 4 {
		t.Fatalf("each ship should fire again after the cooldown, got %d shots", len(f.bullets))
	}
}

func TestSettingsAreClamped(t *testing.T) {
	bad := npcSettings{
		EnemyShips:              -5,
		EnemyShipLife:           0,
		EnemyShipSpeed:          9999,
		EnemyShipFireRateMs:     0,
		MaxBlackHoles:           -1,
		BlackHoleSpawnPeriodSec: 0,
	}
	got := bad.sanitized()

	if got.EnemyShips != 0 {
		t.Fatalf("negative ship count should clamp to 0, got %d", got.EnemyShips)
	}
	if got.EnemyShipSpeed != clientSpeedMax {
		t.Fatalf("speed should clamp to a player's top speed, got %v", got.EnemyShipSpeed)
	}
	if got.EnemyShipLife != defaultNpcSettings().EnemyShipLife {
		t.Fatalf("zero life should fall back to the default, got %v", got.EnemyShipLife)
	}
	// A zero fire rate would fire every tick and a zero spawn period would
	// spawn a black hole every tick, so neither may survive sanitizing.
	if got.EnemyShipFireRateMs < minFireRateMs {
		t.Fatalf("fire rate should clamp to at least %d, got %d", minFireRateMs, got.EnemyShipFireRateMs)
	}
	if got.BlackHoleSpawnPeriodSec < minBlackHoleSpawnPeriodSec {
		t.Fatalf("spawn period should clamp to at least %d, got %d", minBlackHoleSpawnPeriodSec, got.BlackHoleSpawnPeriodSec)
	}
	if got.MaxBlackHoles != 0 {
		t.Fatalf("negative black hole cap should clamp to 0, got %d", got.MaxBlackHoles)
	}

	tooMany := npcSettings{EnemyShips: 1000}.sanitized()
	if tooMany.EnemyShips != maxEnemyShips {
		t.Fatalf("ship count should clamp to %d, got %d", maxEnemyShips, tooMany.EnemyShips)
	}
}

func TestBlackHoleCapIsHonoured(t *testing.T) {
	f := &fakeSender{}
	settings := defaultNpcSettings()
	settings.MaxBlackHoles = 2
	settings.BlackHoleSpawnPeriodSec = 1
	s := settingsSim(f, settings)

	// Black holes only spawn with more than one player present.
	players := map[string]PlayerData{
		"p1": {SocketId: "p1", ShipId: "s1"},
		"p2": {SocketId: "p2", ShipId: "s1", X: 500},
	}
	for i := 0; i < 10; i++ {
		s.lastSpawn = time.Time{}
		s.tick(players)
	}

	if len(s.npcs) > 2 {
		t.Fatalf("black holes should be capped at 2, got %d", len(s.npcs))
	}
	if len(s.npcs) == 0 {
		t.Fatal("expected black holes to spawn")
	}
}
