package main

import (
	"encoding/json"
	"math"
	"sort"
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
		ShipLife:                0,
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
	if got.ShipLife != defaultNpcSettings().ShipLife {
		t.Fatalf("zero life should fall back to the default, got %v", got.ShipLife)
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
	s.settings.ShipLife = 20

	original := theShip(s)
	original.Kills = 3
	originalName := original.Name

	s.handleNpcHit(npcHitMsg{NpcId: "npc1", From: "player1", BulletCharge: 999})
	if len(s.enemyShips) != 0 {
		t.Fatal("ship should have been destroyed")
	}

	players := map[string]PlayerData{"player1": {SocketId: "player1", ShipId: "s1"}}
	// Respawn is delayed by a death, so fast-forward past it.
	for _, retired := range s.retiredShips[controllerRule] {
		retired.respawnAt = time.Now().Add(-time.Second)
	}
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
func TestAttackMatrixSurvivesSanitized(t *testing.T) {
	for _, want := range []bool{true, false} {
		// Deliberately out-of-range everywhere else, so sanitized() has
		// plenty to rewrite while it is at it.
		in := npcSettings{
			EnemyShips:              9999,
			ShipLife:                -1,
			EnemyShipSpeed:          0,
			EnemyShipFireRateMs:     0,
			MaxBlackHoles:           -5,
			BlackHoleSpawnPeriodSec: 0,
			NpcAttacksNpc:           want,
			AiAttacksPlayers:        want,
		}
		got := in.sanitized()
		if got.NpcAttacksNpc != want || got.AiAttacksPlayers != want {
			t.Fatalf("sanitized() changed the attack matrix: got %+v want %v", got, want)
		}
	}
}

// ships-go sends the whole struct; decoding it here must set every cell of
// the attack matrix, and the controller choice with it.
func TestAttackMatrixDecodesFromNpcConfig(t *testing.T) {
	var msg struct {
		Settings npcSettings `json:"settings"`
	}
	raw := `{"eventName":"npcConfig","settings":{"enemyShipController":"both","enemyShips":2,` +
		`"aiShips":3,"npcAttacksPlayers":true,"npcAttacksNpc":true,"npcAttacksAi":true,` +
		`"aiAttacksPlayers":true,"aiAttacksNpc":true,"aiAttacksAi":true}}`
	if err := json.Unmarshal([]byte(raw), &msg); err != nil {
		t.Fatal(err)
	}
	got := msg.Settings
	if got.EnemyShipController != controllerBoth || got.AiShips != 3 {
		t.Fatalf("controller/counts did not decode: %+v", got)
	}
	if !got.NpcAttacksPlayers || !got.NpcAttacksNpc || !got.NpcAttacksAi ||
		!got.AiAttacksPlayers || !got.AiAttacksNpc || !got.AiAttacksAi {
		t.Fatalf("expected every cell of the matrix to decode as on: %+v", got)
	}
}

// Every ship is normalised to the standard size, exactly as ships-vue's
// Player.calculateScale does it, so a ship drawn on a big canvas is not
// flown as if it were bigger than a player can see it. The scale is the
// collision box, so this is a gameplay rule and not a cosmetic one.
func TestShipsAreScaledToTheStandardSize(t *testing.T) {
	f := &fakeSender{}
	settings := defaultNpcSettings()
	settings.EnemyShips = 3
	// The test ship is 100x200, so its largest side is 200.
	s := settingsSim(f, settings)
	s.manageFleet(controllerRule, somePlayers(), time.Now())

	if len(s.enemyShips) == 0 {
		t.Fatal("no ships spawned")
	}
	for _, enemyShip := range s.enemyShips {
		if want := 100.0 / 200.0; enemyShip.Scale != want {
			t.Fatalf("ship scale: got %v want %v", enemyShip.Scale, want)
		}
		// Radius follows from the scale, and is what ramming, crowding and
		// target size are all measured with.
		if want := math.Hypot(50*0.5, 100*0.5); enemyShip.radius() != want {
			t.Fatalf("ship radius: got %v want %v", enemyShip.radius(), want)
		}
	}
}

// Changing the size from the admin panel has to reach the ships already
// flying, or the fleet on the map keeps its old geometry while
// reinforcements arrive with the new one.
func TestShipSizeChangeRescalesTheExistingFleet(t *testing.T) {
	f := &fakeSender{}
	settings := defaultNpcSettings()
	settings.EnemyShips = 2
	s := settingsSim(f, settings)
	s.manageFleet(controllerRule, somePlayers(), time.Now())

	settings.ShipSize = 400
	s.applySettings(settings)

	if len(s.enemyShips) == 0 {
		t.Fatal("no ships spawned")
	}
	for _, enemyShip := range s.enemyShips {
		if want := 400.0 / 200.0; enemyShip.Scale != want {
			t.Fatalf("existing ship not rescaled: got %v want %v", enemyShip.Scale, want)
		}
	}
}

func TestShipSizeDefaultsAndClamps(t *testing.T) {
	if got := defaultNpcSettings().ShipSize; got != defaultShipSize {
		t.Fatalf("default ship size: got %d want %d", got, defaultShipSize)
	}
	cases := []struct {
		name string
		in   int
		want int
	}{
		// Unset (an older ships-go) must mean the default, not the
		// minimum: clamping up from zero would shrink every ship.
		{"unset", 0, defaultShipSize},
		{"negative", -50, defaultShipSize},
		{"too small", 1, minShipSize},
		{"too big", 99999, maxShipSize},
		{"kept", 250, 250},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			settings := defaultNpcSettings()
			settings.ShipSize = c.in
			if got := settings.sanitized().ShipSize; got != c.want {
				t.Fatalf("got %d want %d", got, c.want)
			}
		})
	}
}

// A black hole's life is read from the settings every tick rather than
// stamped on it at spawn, so shortening it from the admin panel retires the
// ones already on the map instead of only applying to the next one.
func TestBlackHoleDurationAppliesToExistingBlackHoles(t *testing.T) {
	f := &fakeSender{}
	settings := defaultNpcSettings()
	settings.EnemyShipController = controllerNone
	s := settingsSim(f, settings)

	addBH(s, 0, 0)
	bh := s.npcs["bh"]
	bh.Scale = 1
	bh.spawnedAt = time.Now().Add(-100 * time.Second)

	// 100s into a 180s life: still fully grown.
	s.tick(nil)
	if s.npcs["bh"] == nil {
		t.Fatal("black hole retired while still within its duration")
	}
	if s.npcs["bh"].Scale != 1 {
		t.Fatalf("black hole shrinking early: scale %v", s.npcs["bh"].Scale)
	}

	settings.BlackHoleDurationSec = 60 // deliberately shorter than the 100s it has lived
	s.applySettings(settings)

	// Now past its (shortened) life, it must shrink away and vanish.
	for i := 0; i < 200 && s.npcs["bh"] != nil; i++ {
		s.tick(nil)
	}
	if s.npcs["bh"] != nil {
		t.Fatalf("black hole outlived a shortened duration: scale %v", s.npcs["bh"].Scale)
	}
}

func TestBlackHoleDurationDefaultsAndClamps(t *testing.T) {
	if got := defaultNpcSettings().BlackHoleDurationSec; got != defaultBlackHoleDurationSec {
		t.Fatalf("default duration: got %d want %d", got, defaultBlackHoleDurationSec)
	}
	cases := []struct {
		name string
		in   int
		want int
	}{
		{"unset", 0, defaultBlackHoleDurationSec},
		{"negative", -1, defaultBlackHoleDurationSec},
		{"too short", 1, minBlackHoleDurationSec},
		{"too long", 999999, maxBlackHoleDurationSec},
		{"kept", 45, 45},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			settings := defaultNpcSettings()
			settings.BlackHoleDurationSec = c.in
			if got := settings.sanitized().BlackHoleDurationSec; got != c.want {
				t.Fatalf("got %d want %d", got, c.want)
			}
		})
	}
}

// The duration a new black hole reports to the clients is the configured
// one, not the constant it used to be.
func TestSpawnedBlackHoleCarriesTheConfiguredDuration(t *testing.T) {
	bh := spawnBlackHole(somePlayers(), time.Now(), 45*time.Second)
	if bh.Duration != 45000 {
		t.Fatalf("duration on the wire: got %d want %d", bh.Duration, 45000)
	}
}

// Black holes used to need two players before one would spawn, so anybody
// playing alone - which is how the fleet settings get tested - never saw a
// single one. Enemy ships have always spawned for a lone player, and a
// black hole is a hazard for them too.
func TestBlackHolesSpawnForASinglePlayer(t *testing.T) {
	f := &fakeSender{}
	settings := defaultNpcSettings()
	settings.EnemyShipController = controllerNone
	settings.BlackHoleSpawnPeriodSec = 1
	s := settingsSim(f, settings)

	players := map[string]PlayerData{"p1": {SocketId: "p1", ShipId: "s1"}}
	s.lastSpawn = time.Time{}
	s.tick(players)

	if len(s.npcs) != 1 {
		t.Fatalf("a lone player got %d black holes, want 1", len(s.npcs))
	}
}

// The size used to be stepped up once per tick, so how fast a black hole
// opened depended on the tick rate and had nothing to do with its
// duration: at a 100ms tick it needed 10s to open at all, which a 15s
// black hole spent almost its whole life doing. It is now derived from
// elapsed time, and collapses so that it is gone exactly on time.
func TestBlackHoleOpensAndClosesWithinItsDuration(t *testing.T) {
	const duration = 15 * time.Second

	full := blackHoleScaleAt(duration/2, duration)
	if full != 1.0 {
		t.Fatalf("mid-life scale: got %v want 1", full)
	}
	if got := blackHoleScaleAt(blackHoleGrowthTime, duration); got != 1.0 {
		t.Fatalf("scale once grown: got %v want 1", got)
	}
	if got := blackHoleScaleAt(0, duration); got != blackHoleScaleStep {
		t.Fatalf("scale at spawn: got %v want %v", got, blackHoleScaleStep)
	}
	if got := blackHoleScaleAt(duration, duration); got != blackHoleScaleStep {
		t.Fatalf("scale at the end: got %v want %v", got, blackHoleScaleStep)
	}

	// A very short one still opens rather than staying a dot.
	if got := blackHoleScaleAt(3*time.Second, 6*time.Second); got != 1.0 {
		t.Fatalf("short black hole never opened: got %v", got)
	}

	// And it is retired when its duration is up, not after an extra
	// tick-counted collapse on top of it.
	f := &fakeSender{}
	settings := defaultNpcSettings()
	settings.EnemyShipController = controllerNone
	settings.BlackHoleDurationSec = 15
	settings.BlackHoleSpawnPeriodSec = 3600
	s := settingsSim(f, settings)

	addBH(s, 0, 0)
	s.npcs["bh"].spawnedAt = time.Now().Add(-14 * time.Second)
	s.tick(nil)
	if s.npcs["bh"] == nil {
		t.Fatal("black hole retired before its duration was up")
	}
	s.npcs["bh"].spawnedAt = time.Now().Add(-15 * time.Second)
	s.tick(nil)
	if s.npcs["bh"] != nil {
		t.Fatal("black hole outlived its duration")
	}
}

// A destroyed fleet used to come back in a single tick: one shared timer
// was pushed forward by every death, and when it finally expired
// manageFleet filled every empty place at once. Each ship now serves its
// own delay, so they trickle back the way players do.
func TestDeadShipsRespawnOneByOneRatherThanAllAtOnce(t *testing.T) {
	f := &fakeSender{}
	settings := defaultNpcSettings()
	settings.EnemyShips = 6
	settings.AiShips = 0
	settings.EnemyShipController = string(controllerRule)
	s := settingsSim(f, settings)

	players := map[string]PlayerData{"p1": {SocketId: "p1", ShipId: "s1"}}
	s.tickEnemyShips(players, time.Now())
	if len(s.enemyShips) != 6 {
		t.Fatalf("fleet did not form: %d ships", len(s.enemyShips))
	}

	// Wipe it out on a single tick, the worst case for a shared timer.
	for _, ship := range s.enemyShips {
		s.handleNpcHit(npcHitMsg{NpcId: ship.Id, From: "p1", BulletCharge: 9999})
	}
	if len(s.enemyShips) != 0 {
		t.Fatalf("fleet should be wiped out, %d left", len(s.enemyShips))
	}

	retired := s.retiredShips[controllerRule]
	if len(retired) != 6 {
		t.Fatalf("retired %d identities, want 6", len(retired))
	}
	// No two of them may be due back on the same tick.
	sameTick := 0
	for i, a := range retired {
		for _, b := range retired[i+1:] {
			if a.respawnAt.Sub(b.respawnAt).Abs() < 50*time.Millisecond {
				sameTick++
			}
		}
	}
	if sameTick > 3 {
		t.Fatalf("%d pairs of ships are due back on the same tick: the fleet still returns as a block", sameTick)
	}

	// Nobody comes back before their delay, and they arrive in the order
	// they are due rather than in one burst.
	now := time.Now()
	s.tickEnemyShips(players, now.Add(enemyShipRespawnDelay-time.Second))
	if len(s.enemyShips) != 0 {
		t.Fatalf("%d ships respawned before their delay", len(s.enemyShips))
	}

	sort.Slice(retired, func(i, j int) bool { return retired[i].respawnAt.Before(retired[j].respawnAt) })
	for i, ship := range retired {
		s.tickEnemyShips(players, ship.respawnAt.Add(time.Millisecond))
		if _, back := s.enemyShips[ship.Id]; !back {
			t.Fatalf("ship %d was not back once its own delay had passed", i)
		}
		if len(s.enemyShips) != i+1 {
			t.Fatalf("after %d delays elapsed, %d ships are back: they are not respawning one by one", i+1, len(s.enemyShips))
		}
	}
}
