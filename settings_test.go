package main

import (
	"encoding/json"
	"math"
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
	// Park both ships in firing range of the player at (0,0), nose already
	// pointing at it - ships only fire when they're lined up.
	for _, ship := range s.enemyShips {
		ship.X, ship.Y = 200, 200
		offsetX, offsetY := ship.ship.centerOffset()
		ship.Rotate = float32(normalizeAngle(math.Atan2(-(ship.Y + offsetY), -(ship.X + offsetX))))
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

// TestShipsDoNotStackOnTopOfEachOther is the regression guard for a fleet
// of ships piling into a single point: ship-vs-ship collision is resolved
// client-side in ships-vue, but a client can only ever move its *own*
// player, so nothing was resolving NPC-against-NPC overlap.
func TestShipsDoNotStackOnTopOfEachOther(t *testing.T) {
	f := &fakeSender{}
	settings := defaultNpcSettings()
	settings.EnemyShips = 8
	s := settingsSim(f, settings)

	players := map[string]PlayerData{"p1": {SocketId: "p1", ShipId: "s1", X: 0, Y: 0}}
	s.tickEnemyShips(players, time.Now())
	// Start them all welded together, the worst case, then let them fly.
	for _, ship := range s.enemyShips {
		ship.X, ship.Y = 4000, 4000
	}
	for i := 0; i < 200; i++ {
		s.tickEnemyShips(players, time.Now())
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
			overlapX := math.Min(a.X+aw, b.X+bw) - math.Max(a.X, b.X)
			overlapY := math.Min(a.Y+ah, b.Y+bh) - math.Max(a.Y, b.Y)
			if overlapX > 0 && overlapY > 0 {
				t.Fatalf("ships %s and %s overlap by (%v, %v)", a.Id, b.Id, overlapX, overlapY)
			}
		}
	}
}

// TestOverlappingShipsArePushedApart checks the collision resolution
// itself, independently of the steering that normally prevents it.
func TestOverlappingShipsArePushedApart(t *testing.T) {
	f := &fakeSender{}
	settings := defaultNpcSettings()
	settings.EnemyShips = 2
	s := settingsSim(f, settings)
	s.tickEnemyShips(somePlayers(), time.Now())

	ships := make([]*enemyShipState, 0, 2)
	for _, ship := range s.enemyShips {
		ships = append(ships, ship)
	}
	ships[0].X, ships[0].Y = 1000, 1000
	ships[1].X, ships[1].Y = 1010, 1000 // deep overlap

	s.separateEnemyShips()

	if ships[0].X == 1000 && ships[1].X == 1010 {
		t.Fatal("overlapping ships were not pushed apart")
	}
	// Both move, by the same amount: neither ship is privileged.
	movedA := math.Abs(ships[0].X - 1000)
	movedB := math.Abs(ships[1].X - 1010)
	if math.Abs(movedA-movedB) > 0.0001 {
		t.Fatalf("push should be symmetric, moved %v and %v", movedA, movedB)
	}
}

// A destroyed ship must come back as the same rival rather than a stranger:
// ships-vue lists NPCs in the scoreboard, and a score that resets on every
// death would be meaningless.
func TestAKilledShipComesBackWithItsIdentityAndScore(t *testing.T) {
	f := &fakeSender{}
	s := newSim(f)
	s.settings.EnemyShips = 1
	s.settings.EnemyShipLife = 20

	original := theShip(s)
	original.Kills = 3
	originalName := original.Name

	s.handleNpcHit(npcHitMsg{NpcId: "npc1", From: "player1", BulletCharge: 999})
	if len(s.enemyShips) != 0 {
		t.Fatal("ship should have been destroyed")
	}

	players := map[string]PlayerData{"player1": {SocketId: "player1", ShipId: "s1"}}
	// Respawn is delayed by a death, so fast-forward past it.
	s.enemyRespawnAt = time.Now().Add(-time.Second)
	s.manageFleetSize(players, time.Now())

	revived := theShip(s)
	if revived == nil {
		t.Fatalf("expected the same ship id back, got %v", s.enemyShips)
	}
	if revived.Name != originalName {
		t.Errorf("name = %q, want %q", revived.Name, originalName)
	}
	if revived.Kills != 3 {
		t.Errorf("kills = %d, want the 3 it had before dying", revived.Kills)
	}
	if revived.Deaths != 1 {
		t.Errorf("deaths = %d, want 1", revived.Deaths)
	}
	if revived.Life != 20 {
		t.Errorf("life = %v, want a full 20 after respawning", revived.Life)
	}
}

// The victim's own client detects the hit, so the only way an NPC learns it
// killed somebody is ships-go relaying the event back.
func TestCreditsAnEnemyShipForThePlayersItKills(t *testing.T) {
	f := &fakeSender{}
	s := newSim(f)

	s.recordKills([]killEventData{
		{From: "npc1", PlayerId: "player1"},
		{From: "npc1", PlayerId: "player2"},
		{From: "player3", PlayerId: "player4"}, // player kills player, not ours
	})

	if got := theShip(s).Kills; got != 2 {
		t.Fatalf("kills = %d, want 2", got)
	}
}

// A player shooting down one of our ships comes back to us as a kill event
// too. Counting it would credit the NPC for its own death.
func TestDoesNotCreditAKillForOneOfItsOwnShipsDying(t *testing.T) {
	f := &fakeSender{}
	s := newSim(f)

	s.recordKills([]killEventData{{From: "player1", PlayerId: "npc1"}})

	if got := theShip(s).Kills; got != 0 {
		t.Fatalf("kills = %d, want 0", got)
	}
}

// Lowering the fleet size from the admin panel retires ships rather than
// discarding them, so raising it again brings the same rivals back.
func TestShrinkingTheFleetKeepsTheRetiredShipsScores(t *testing.T) {
	f := &fakeSender{}
	s := newSim(f)
	theShip(s).Kills = 5
	s.settings.EnemyShips = 0

	players := map[string]PlayerData{"player1": {SocketId: "player1", ShipId: "s1"}}
	s.manageFleetSize(players, time.Now())
	if len(s.enemyShips) != 0 {
		t.Fatal("fleet should be empty")
	}

	s.settings.EnemyShips = 1
	s.manageFleetSize(players, time.Now())

	revived := theShip(s)
	if revived == nil || revived.Kills != 5 {
		t.Fatalf("expected npc1 back with 5 kills, got %+v", s.enemyShips)
	}
	if revived.Deaths != 0 {
		t.Errorf("deaths = %d, want 0: despawning is not dying", revived.Deaths)
	}
}

// The toggle is pushed from ships-go as an npcConfig and must survive
// sanitized(), which is the only thing standing between the wire and the
// simulation. Clamping other fields must never quietly reset it.
func TestFightEachOtherSurvivesSanitized(t *testing.T) {
	for _, want := range []bool{true, false} {
		// Deliberately out-of-range everywhere else, so sanitized() has
		// plenty to rewrite while it is at it.
		in := npcSettings{
			EnemyShips:               9999,
			EnemyShipLife:            -1,
			EnemyShipSpeed:           0,
			EnemyShipFireRateMs:      0,
			MaxBlackHoles:            -5,
			BlackHoleSpawnPeriodSec:  0,
			EnemyShipsFightEachOther: want,
		}
		if got := in.sanitized().EnemyShipsFightEachOther; got != want {
			t.Fatalf("sanitized() changed the toggle: got %v want %v", got, want)
		}
	}
}

// ships-go sends the whole struct; decoding it here must set the toggle.
func TestFightEachOtherDecodesFromNpcConfig(t *testing.T) {
	var msg struct {
		Settings npcSettings `json:"settings"`
	}
	raw := `{"eventName":"npcConfig","settings":{"enemyShips":2,"enemyShipsFightEachOther":true}}`
	if err := json.Unmarshal([]byte(raw), &msg); err != nil {
		t.Fatal(err)
	}
	if !msg.Settings.EnemyShipsFightEachOther {
		t.Fatal("expected the toggle to decode as on")
	}
}
