package main

import (
	"encoding/json"
	"math"
	"testing"
	"time"
)

type fakeSender struct {
	died    []playerDiedMsg
	bullets []newBulletMsg
}

func (f *fakeSender) sendBullet(b newBulletMsg) error      { f.bullets = append(f.bullets, b); return nil }
func (f *fakeSender) sendRemoveBullet(id string) error     { return nil }
func (f *fakeSender) sendPlayerDied(m playerDiedMsg) error { f.died = append(f.died, m); return nil }

func newSim(f *fakeSender) *npcSimulator {
	ship := publicShip{Id: "s1", Name: "T", Width: 100, Height: 200}
	s := newNpcSimulator(f, []publicShip{ship}, 100*time.Millisecond)
	s.enemyShips["npc1"] = &enemyShipState{ship: ship, spawnedAt: time.Now(), NpcData: NpcData{
		Type: NpcTypes.Ship, Id: "npc1", ShipId: "s1", Scale: 1, Life: 20, MaxLife: 20,
	}}
	return s
}

// theShip is the single enemy ship newSim seeds, for tests that drive one
// ship directly rather than going through fleet management.
func theShip(s *npcSimulator) *enemyShipState { return s.enemyShips["npc1"] }

func addBH(s *npcSimulator, x, y float64) {
	s.npcs["bh"] = &npcState{spawnedAt: time.Now(), NpcData: NpcData{
		Type: NpcTypes.BlackHole, Id: "bh", X: x, Y: y, Scale: 1, MaxSize: 800,
	}}
}

func TestPullRateMatchesClient(t *testing.T) {
	f := &fakeSender{}
	s := newSim(f)
	// black hole center at (0,0) -> top-left -400,-400
	addBH(s, -400, -400)
	// ship center at (3000, 0)
	theShip(s).X, theShip(s).Y = 1500-50, 0-100
	before := theShip(s).X
	s.applyBlackHoleGravity(theShip(s), 50, 100, theShip(s).radius())
	moved := before - theShip(s).X
	// client per-frame step at this edge distance, x3 for the 100ms tick
	edge := 1500 - (400*math.Sqrt2 + math.Hypot(50, 100))
	want := (1000 / edge) * 1.25 * 3
	if math.Abs(moved-want) > 0.001 {
		t.Fatalf("pull %v, want %v (edge %v)", moved, want, edge)
	}
	if len(f.died) != 0 {
		t.Fatal("should not have died")
	}
}

func TestOutOfRangeNoPull(t *testing.T) {
	f := &fakeSender{}
	s := newSim(f)
	addBH(s, -400, -400)
	theShip(s).X, theShip(s).Y = 50000, 0
	before := theShip(s).X
	s.applyBlackHoleGravity(theShip(s), 50, 100, theShip(s).radius())
	if before != theShip(s).X {
		t.Fatal("should not be pulled from far away")
	}
}

func TestSwallowedDies(t *testing.T) {
	f := &fakeSender{}
	s := newSim(f)
	addBH(s, -400, -400)
	theShip(s).X, theShip(s).Y = -50, -100 // center exactly on bh center
	s.applyBlackHoleGravity(theShip(s), 50, 100, theShip(s).radius())
	if len(f.died) != 1 || f.died[0].PlayerId != "npc1" || f.died[0].From != "" {
		t.Fatalf("expected a no-killer death, got %+v", f.died)
	}
	if theShip(s) != nil {
		t.Fatal("ship should be gone")
	}
	if !time.Now().Before(s.enemyRespawnAt) {
		t.Fatal("respawn timer not set")
	}
}

func TestFleesInsteadOfChasingAndHoldsFire(t *testing.T) {
	f := &fakeSender{}
	s := newSim(f)
	addBH(s, -400, -400) // center (0,0)
	// ship center at (2400,0): edge distance ~1722 -> inside the fear radius
	theShip(s).X, theShip(s).Y = 2400-50, 0-100
	// player camping the black hole: the bait it must not take
	players := map[string]PlayerData{"p1": {SocketId: "p1", ShipId: "s1", X: 0, Y: 0}}

	s.tickEnemyShip(theShip(s), players, time.Now())
	if !theShip(s).fleeing {
		t.Fatal("should be fleeing")
	}
	if len(f.bullets) != 0 {
		t.Fatal("should hold fire while escaping")
	}
	// It turns towards "away from the black hole" (angle 0) at the fixed
	// player turn rate rather than snapping to it, so all we can assert
	// after one tick is that it turned the short way round.
	if theShip(s).Rotate != 0 {
		t.Fatalf("already facing away, should not have turned, got %v", theShip(s).Rotate)
	}

	// It can never reach a player sitting on a black hole, and it must not
	// be dragged in either. Since turning now takes time it does lose
	// ground at first, so what matters is that it ends up further out than
	// the gravity well rather than never giving any ground at all.
	// The hysteresis makes it cycle: run clear of the well, turn back for
	// the bait, get scared again. So a snapshot at any single tick lands
	// somewhere in that cycle - what matters is that it does break free at
	// some point, and is never dragged near the event horizon.
	bestEdge := -math.MaxFloat64
	worstEdge := math.MaxFloat64
	for i := 0; i < 400; i++ {
		s.tickEnemyShip(theShip(s), players, time.Now())
		if theShip(s) == nil {
			t.Fatalf("should have escaped, was swallowed at tick %d", i)
		}
		offsetX, offsetY := theShip(s).ship.centerOffset()
		_, _, edge, _ := s.nearestBlackHole(theShip(s).X+offsetX, theShip(s).Y+offsetY, theShip(s).radius())
		bestEdge = math.Max(bestEdge, edge)
		worstEdge = math.Min(worstEdge, edge)
	}
	if bestEdge < enemyShipBlackHoleFear {
		t.Fatalf("never got clear of the black hole, best edge distance %v", bestEdge)
	}
	if worstEdge < 300 {
		t.Fatalf("got dragged dangerously close, worst edge distance %v", worstEdge)
	}
	if len(f.died) != 0 {
		t.Fatal("should not have been swallowed")
	}
}

func TestChasesWhenNoBlackHoleNear(t *testing.T) {
	f := &fakeSender{}
	s := newSim(f)
	theShip(s).X, theShip(s).Y = 5000, 0
	players := map[string]PlayerData{"p1": {SocketId: "p1", ShipId: "s1", X: 0, Y: 0}}
	// It spawns facing +x with the player behind it at the origin, so it
	// has to turn around first - it no longer snaps to the bearing.
	for i := 0; i < 100; i++ {
		s.tickEnemyShip(theShip(s), players, time.Now())
	}
	if theShip(s).X >= 5000 {
		t.Fatalf("should close in on the player, x=%v", theShip(s).X)
	}
}

// TestTurnsProgressively pins the turn rate to a player's: a ship must
// never snap to a new heading, however far off it is.
func TestTurnsProgressively(t *testing.T) {
	f := &fakeSender{}
	s := newSim(f)
	theShip(s).Rotate = 0
	// Player directly behind it: a half-turn away.
	players := map[string]PlayerData{"p1": {SocketId: "p1", ShipId: "s1", X: -5000, Y: -100}}
	theShip(s).X, theShip(s).Y = -50, -100

	step := clientSpeedRotation * s.frameScale
	previous := 0.0
	for i := 0; i < 10; i++ {
		s.tickEnemyShip(theShip(s), players, time.Now())
		turned := math.Abs(angleDifference(previous, float64(theShip(s).Rotate)))
		if turned > step+0.0001 {
			t.Fatalf("tick %d turned %v rad, more than one step of %v", i, turned, step)
		}
		previous = float64(theShip(s).Rotate)
	}
	if previous == 0 {
		t.Fatal("should have started turning towards the player")
	}
	if math.Abs(angleDifference(previous, math.Pi)) < 0.0001 {
		t.Fatal("should not have reached the target heading yet")
	}
}

// clientBulletDirection replicates ships-vue's Bullet constructor exactly.
func clientBulletDirection(angle float64) (float64, float64) {
	quad := int(angle / (math.Pi / 2))
	moveX := math.Abs(math.Cos(angle))
	moveY := math.Abs(math.Sin(angle))
	switch quad {
	case 1:
		moveX *= -1
	case 2:
		moveY *= -1
		moveX *= -1
	case 3:
		moveY *= -1
	}
	return moveX, moveY
}

func TestAimsAtTargetFromEveryDirection(t *testing.T) {
	f := &fakeSender{}
	ship := publicShip{Id: "s1", Name: "T", Width: 100, Height: 200}
	s := newNpcSimulator(f, []publicShip{ship}, 100*time.Millisecond)

	for deg := 0; deg < 360; deg += 45 {
		f.bullets = nil
		s.enemyShips["npc1"] = &enemyShipState{ship: ship, spawnedAt: time.Now(), NpcData: NpcData{
			Type: NpcTypes.Ship, Id: "npc1", ShipId: "s1", Scale: 1, Life: 20, MaxLife: 20,
			X: -50, Y: -100, // center (0,0)
		}}
		theShip(s).lastShotAt = time.Time{}

		rad := float64(deg) * math.Pi / 180
		// Guns fire along the ship's nose now, so line it up first: this
		// test is about the direction maths on the wire, not about turning.
		theShip(s).Rotate = float32(rad)
		// target center 1000px away at `deg`
		tcx, tcy := 1000*math.Cos(rad), 1000*math.Sin(rad)
		players := map[string]PlayerData{"p1": {SocketId: "p1", ShipId: "s1",
			X: float32(tcx - 50), Y: float32(tcy - 100)}}

		s.tickEnemyShip(theShip(s), players, time.Now())
		if len(f.bullets) != 1 {
			t.Fatalf("deg %d: expected a shot, got %d", deg, len(f.bullets))
		}
		b := f.bullets[0]
		mx, my := clientBulletDirection(float64(b.Angle))
		wantX, wantY := math.Cos(rad), math.Sin(rad)
		if math.Abs(mx-wantX) > 0.01 || math.Abs(my-wantY) > 0.01 {
			t.Errorf("deg %d: bullet flies (%v,%v), want (%v,%v)", deg, mx, my, wantX, wantY)
		}
	}
}

// TestCatchesAFleeingPlayer guards the speed model against regressing to
// hardcoded values: the ship used to cruise ~6x slower than its intended
// speed, so it trailed forever at a near-constant bearing (looking like it
// wasn't rotating) and never got inside firing range.
//
// The player here runs away at a bit less than the ship's *configured*
// cruising speed, so this asserts the ship actually reaches the speed it
// was configured with. It deliberately does not assert that a ship can
// catch a player at full throttle: with the default speed (20 against
// ships-vue's SPEED.MAX of 50) it can't, by design - a player at full
// throttle is meant to be able to escape.
func TestCatchesAFleeingPlayer(t *testing.T) {
	f := &fakeSender{}
	s := newSim(f)
	theShip(s).X, theShip(s).Y = 3000-50, -100 // center (3000, 0)

	playerX := 0.0
	playerSpeed := s.maxSpeed * 0.6
	start := math.Hypot(3000, 0)

	for i := 0; i < 400; i++ {
		playerX -= playerSpeed
		players := map[string]PlayerData{"p1": {SocketId: "p1", ShipId: "s1", X: float32(playerX - 50), Y: -100}}
		s.tickEnemyShip(theShip(s), players, time.Now())
	}

	gap := math.Abs(theShip(s).X + 50 - playerX)
	if gap >= start {
		t.Fatalf("never closed the gap: %v -> %v", start, gap)
	}
	if len(f.bullets) == 0 {
		t.Fatal("should have got into firing range and shot")
	}
}

// TestAPlayerAtFullThrottleCanEscape is the flip side: the default speed is
// a balance decision (an admin can raise it to SPEED.MAX to remove the
// escape hatch), so it's worth pinning that the default leaves one.
func TestAPlayerAtFullThrottleCanEscape(t *testing.T) {
	f := &fakeSender{}
	s := newSim(f)
	theShip(s).X, theShip(s).Y = 1000-50, -100

	playerX := 0.0
	playerSpeed := clientSpeedMax * s.frameScale
	for i := 0; i < 200; i++ {
		playerX -= playerSpeed
		players := map[string]PlayerData{"p1": {SocketId: "p1", ShipId: "s1", X: float32(playerX - 50), Y: -100}}
		s.tickEnemyShip(theShip(s), players, time.Now())
	}

	if gap := math.Abs(theShip(s).X + 50 - playerX); gap <= 1000 {
		t.Fatalf("a player at full throttle should outrun the default ship, gap %v", gap)
	}
}

// TestNpcUpdateAlwaysCarriesRotate pins the wire format for a field where
// the zero value is meaningful. A ship spawns pointing due east (rotate 0),
// and with `omitempty` that heading vanished from the JSON; ships-vue's
// update path assigned the missing value straight to npc.rotate, so every
// position and collision calculation for that ship became NaN and it went
// invisible and unhittable.
func TestNpcUpdateAlwaysCarriesRotate(t *testing.T) {
	raw, err := json.Marshal(npcUpdateMsg{
		EventName: "npcUpdate",
		Npcs:      []NpcData{{Type: NpcTypes.Ship, Id: "npc-1", Rotate: 0, Life: 10, MaxLife: 10}},
	})
	if err != nil {
		t.Fatalf("marshal failed: %v", err)
	}

	var decoded struct {
		Npcs []map[string]any `json:"npcs"`
	}
	if err := json.Unmarshal(raw, &decoded); err != nil {
		t.Fatalf("unmarshal failed: %v", err)
	}
	if len(decoded.Npcs) != 1 {
		t.Fatalf("expected 1 npc, got %d", len(decoded.Npcs))
	}
	for _, field := range []string{"rotate", "kills", "deaths"} {
		if _, ok := decoded.Npcs[0][field]; !ok {
			t.Errorf("%q missing from npcUpdate for a zero value; ships-vue reads it as undefined: %s", field, raw)
		}
	}
}

// addShip seeds a second enemy ship, for the NPC-vs-NPC tests.
func addShip(s *npcSimulator, id string, x, y float64) *enemyShipState {
	ship := s.shipsById["s1"]
	e := &enemyShipState{ship: ship, spawnedAt: time.Now(), NpcData: NpcData{
		Type: NpcTypes.Ship, Id: id, ShipId: "s1", Name: id, X: x, Y: y, Scale: 1, Life: 20, MaxLife: 20,
	}}
	s.enemyShips[id] = e
	return e
}

func withFriendlyFire(s *npcSimulator, on bool) {
	settings := s.settings
	settings.EnemyShipsFightEachOther = on
	s.settings = settings
}

// With the toggle off, a rival ship is invisible to the targeting code even
// when it is far closer than any player.
func TestOnlyTargetsPlayersWhenFriendlyFireIsOff(t *testing.T) {
	s := newSim(&fakeSender{})
	withFriendlyFire(s, false)
	addShip(s, "npc2", 200, 0)
	players := map[string]PlayerData{"p1": {SocketId: "p1", ShipId: "s1", X: 9000, Y: 0}}

	target, found := s.nearestTarget(theShip(s), 0, 0, players)
	if !found {
		t.Fatal("expected the player to be targeted")
	}
	if target.isShip || target.id != "p1" {
		t.Fatalf("expected the distant player, got %+v", target)
	}
}

func TestTargetsNearestRivalShipWhenFriendlyFireIsOn(t *testing.T) {
	s := newSim(&fakeSender{})
	withFriendlyFire(s, true)
	addShip(s, "npc2", 200, 0)
	players := map[string]PlayerData{"p1": {SocketId: "p1", ShipId: "s1", X: 9000, Y: 0}}

	target, found := s.nearestTarget(theShip(s), 0, 0, players)
	if !found || !target.isShip || target.id != "npc2" {
		t.Fatalf("expected the nearby rival ship, got %+v (found=%v)", target, found)
	}

	// A nearer player still wins: this is "also attack each other", not
	// "ignore players".
	players["p1"] = PlayerData{SocketId: "p1", ShipId: "s1", X: 10, Y: 0}
	target, _ = s.nearestTarget(theShip(s), 0, 0, players)
	if target.isShip {
		t.Fatalf("a closer player should still win, got %+v", target)
	}

	// And a ship never shoots at itself.
	self := theShip(s)
	self.X, self.Y = 0, 0
	delete(s.enemyShips, "npc2")
	target, found = s.nearestTarget(self, 0, 0, map[string]PlayerData{})
	if found {
		t.Fatalf("a lone ship has nothing to target, got %+v", target)
	}
}

// The whole point of the feature: one NPC's bullets must actually damage
// and eventually destroy another, which nobody's client would ever report.
func TestNpcBulletsDamageRivalShipsOnlyWhenEnabled(t *testing.T) {
	for _, friendlyFire := range []bool{false, true} {
		f := &fakeSender{}
		s := newSim(f)
		withFriendlyFire(s, friendlyFire)

		shooter := theShip(s)
		shooter.X, shooter.Y = 0, 0
		shooter.Rotate = 0 // due east
		victim := addShip(s, "npc2", 600, -100)
		victim.Life = npcBulletCharge // one hit from death

		now := time.Now()
		s.fireAt(shooter, now)
		if len(f.bullets) != 1 {
			t.Fatalf("expected one bullet, got %d", len(f.bullets))
		}

		// Fly it far enough to cross the victim and out the other side.
		for i := 0; i < 40; i++ {
			s.advanceBullets(now)
		}

		_, stillAlive := s.enemyShips["npc2"]
		if friendlyFire && stillAlive {
			t.Fatal("with friendly fire on, the rival should have been destroyed")
		}
		if !friendlyFire && !stillAlive {
			t.Fatal("with friendly fire off, NPC bullets must not damage other NPCs")
		}
		if got := len(f.died); friendlyFire && got != 1 {
			t.Fatalf("expected exactly one playerDied, got %d", got)
		} else if !friendlyFire && got != 0 {
			t.Fatalf("expected no playerDied, got %d", got)
		}
		if friendlyFire {
			if f.died[0].From != shooter.Id || f.died[0].PlayerId != "npc2" {
				t.Fatalf("kill feed must name the shooter as killer: %+v", f.died[0])
			}
			if shooter.Kills != 1 {
				t.Fatalf("shooter should be credited one kill, got %d", shooter.Kills)
			}
			if victim.Deaths != 1 {
				t.Fatalf("victim should be credited one death, got %d", victim.Deaths)
			}
		}
	}
}

// A bullet starts inside the hull of the ship that fired it, so without an
// explicit exemption every shot would be instantly fatal to the shooter.
func TestAShipIsNeverKilledByItsOwnBullet(t *testing.T) {
	f := &fakeSender{}
	s := newSim(f)
	withFriendlyFire(s, true)

	shooter := theShip(s)
	shooter.X, shooter.Y = 0, 0

	now := time.Now()
	s.fireAt(shooter, now)
	for i := 0; i < 10; i++ {
		s.advanceBullets(now)
	}

	if _, ok := s.enemyShips["npc1"]; !ok {
		t.Fatal("a ship shot itself dead")
	}
	if len(f.died) != 0 {
		t.Fatalf("no death expected, got %+v", f.died)
	}
}

// Hit detection has to sweep the segment a bullet covered this tick, not
// test where it ended up: NPC_TICK_INTERVAL_MS is configurable, and at a
// slow tick a bullet crosses far more than a ship's width per step.
func TestFastBulletsDoNotTunnelThroughShips(t *testing.T) {
	f := &fakeSender{}
	ship := publicShip{Id: "s1", Name: "T", Width: 100, Height: 200}
	// A deliberately slow tick, so one bullet step dwarfs a ship.
	s := newNpcSimulator(f, []publicShip{ship}, 500*time.Millisecond)
	withFriendlyFire(s, true)

	shooter := &enemyShipState{ship: ship, spawnedAt: time.Now(), NpcData: NpcData{
		Type: NpcTypes.Ship, Id: "npc1", ShipId: "s1", Scale: 1, Life: 20, MaxLife: 20,
	}}
	s.enemyShips["npc1"] = shooter

	step := npcBulletSpeed * s.frameScale
	if step <= 2*shooter.radius() {
		t.Fatalf("test is not exercising tunnelling: step %v vs ship radius %v", step, shooter.radius())
	}

	// Centered halfway between two consecutive bullet positions, and far
	// enough from both that a point test at either one misses it.
	offsetX, offsetY := ship.centerOffset()
	victim := addShip(s, "npc2", step*2.5-offsetX, -offsetY)
	victim.Life = npcBulletCharge

	now := time.Now()
	s.fireAt(shooter, now)
	for i := 0; i < 5; i++ {
		s.advanceBullets(now)
	}

	if _, stillAlive := s.enemyShips["npc2"]; stillAlive {
		t.Fatal("bullet tunnelled straight through the rival ship")
	}
}

// Bullets must still be retired when they miss, or the tracking map grows
// for the life of the process.
func TestBulletsAreRetiredAfterTheirLifetime(t *testing.T) {
	f := &fakeSender{}
	s := newSim(f)
	withFriendlyFire(s, true)

	now := time.Now()
	s.fireAt(theShip(s), now)
	if len(s.activeBullets) != 1 {
		t.Fatalf("expected the bullet to be tracked, got %d", len(s.activeBullets))
	}

	s.advanceBullets(now.Add(npcBulletLifetime))
	if len(s.activeBullets) != 0 {
		t.Fatalf("expected the bullet to be retired, got %d", len(s.activeBullets))
	}
}

// GET /game/getShips only lists public ships, so a player flying one of
// their own painting projects has a shipId this service has never seen.
// Resolving that to a zero center offset made every NPC aim at the ship's
// top-left corner instead of its middle - a fixed 25-35 degree error that
// looked like the NPC turning to the wrong place and never landing a shot.
func TestAimsAtCenterOfAPlayerFlyingACustomShip(t *testing.T) {
	for _, tc := range []struct {
		name   string
		px, py float64
	}{
		{"east", 8000, 0},
		{"north west", -3000, -3000},
		{"south east", 2500, 4000},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := &fakeSender{}
			s := newSim(f)
			sh := theShip(s)
			sh.X, sh.Y = 0, 0

			now := time.Now()
			players := map[string]PlayerData{"p1": {
				SocketId: "p1", ShipId: "a-ship-of-their-own", X: float32(tc.px), Y: float32(tc.py),
			}}
			for i := 0; i < 900; i++ {
				now = now.Add(100 * time.Millisecond)
				s.tickEnemyShip(sh, players, now)
			}

			// newSim's only public ship is 100x200, so the fallback puts the
			// player's center half a ship down and right of its anchor.
			cx, cy := sh.center()
			bearing := math.Atan2(tc.py+100-cy, tc.px+50-cx)
			off := math.Abs(angleDifference(float64(sh.Rotate), bearing))
			if off > 0.02 {
				t.Fatalf("settled %.1f degrees off the player's center", off*180/math.Pi)
			}
		})
	}
}

func TestPickFallbackShipPrefersAPublicOne(t *testing.T) {
	mine := publicShip{Id: "mine", UserId: "u1"}
	public := publicShip{Id: "public"}
	if got, ok := pickFallbackShip([]publicShip{mine, public}); !ok || got.Id != "public" {
		t.Fatalf("got %+v (ok=%v), want the public ship", got, ok)
	}
	if got, ok := pickFallbackShip([]publicShip{mine}); !ok || got.Id != "mine" {
		t.Fatalf("got %+v (ok=%v), want the only ship there is", got, ok)
	}
	if _, ok := pickFallbackShip(nil); ok {
		t.Fatal("no ships means no fallback")
	}
}

// Reproduces the live case that made NPCs look like they were turning
// somewhere else entirely: a player flying a 1000x1000 ship of their own
// making, drawn at scale 0.18 because of their score. Its top-left anchor
// - which is all that travels on the wire - is 500px from its middle, and
// no ship in the public list is anywhere near that big, so guessing the
// geometry from GET /game/getShips put the aim point hundreds of pixels
// away and made the aim tolerance several times too generous.
func TestAimsAtACustomShipUsingTheSizeOnTheWire(t *testing.T) {
	f := &fakeSender{}
	s := newSim(f)
	sh := theShip(s)
	sh.X, sh.Y = 0, 0

	p := PlayerData{
		SocketId: "p1", ShipId: "a-painting-project-of-their-own",
		X: 6000, Y: 6000, Width: 1000, Height: 1000, Scale: 0.18,
	}

	cx, cy := s.playerCenter(p)
	if cx != 6500 || cy != 6500 {
		t.Fatalf("center (%v,%v), want (6500,6500)", cx, cy)
	}
	// The drawn ship is 180x180, so the tolerance must come from that and
	// not from the raw 1000x1000 (which is 5.5x too forgiving).
	if got, want := s.playerRadius(p), math.Hypot(90, 90); math.Abs(got-want) > 0.001 {
		t.Fatalf("radius %v, want %v", got, want)
	}

	now := time.Now()
	players := map[string]PlayerData{"p1": p}
	for i := 0; i < 900; i++ {
		now = now.Add(100 * time.Millisecond)
		s.tickEnemyShip(sh, players, now)
	}

	sx, sy := sh.center()
	off := math.Abs(angleDifference(float64(sh.Rotate), math.Atan2(6500-sy, 6500-sx)))
	if off > 0.02 {
		t.Fatalf("settled %.1f degrees off the player's center", off*180/math.Pi)
	}
	if len(f.bullets) == 0 {
		t.Fatal("never fired")
	}
}

// A scale of 0 means "the client didn't tell us", not "zero sized".
func TestMissingScaleIsTreatedAsFullSize(t *testing.T) {
	s := newSim(&fakeSender{})
	p := PlayerData{ShipId: "unknown", X: 0, Y: 0, Width: 400, Height: 200}
	if got, want := s.playerRadius(p), math.Hypot(200, 100); math.Abs(got-want) > 0.001 {
		t.Fatalf("radius %v, want %v", got, want)
	}
}

// Clients that predate width/height on the wire must still be aimed at.
func TestFallsBackToTheShipListWhenSizeIsAbsent(t *testing.T) {
	s := newSim(&fakeSender{})
	p := PlayerData{ShipId: "s1", X: 100, Y: 100} // newSim's only ship is 100x200
	if cx, cy := s.playerCenter(p); cx != 150 || cy != 200 {
		t.Fatalf("center (%v,%v), want (150,200)", cx, cy)
	}
}
