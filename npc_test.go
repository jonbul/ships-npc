package main

import (
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
	if math.Abs(float64(theShip(s).Rotate)) > 0.001 {
		t.Fatalf("should face away from the black hole (angle 0), got %v", theShip(s).Rotate)
	}

	// It can never reach a player sitting on a black hole, but it must
	// never be dragged in either: it should settle into approaching and
	// retreating around the fear radius rather than diving in.
	worstEdge := math.MaxFloat64
	for i := 0; i < 300; i++ {
		s.tickEnemyShip(theShip(s), players, time.Now())
		if theShip(s) == nil {
			t.Fatalf("should have escaped, was swallowed at tick %d", i)
		}
		offsetX, offsetY := theShip(s).ship.centerOffset()
		_, _, edge, _ := s.nearestBlackHole(theShip(s).X+offsetX, theShip(s).Y+offsetY, theShip(s).radius())
		worstEdge = math.Min(worstEdge, edge)
	}
	if worstEdge < enemyShipBlackHoleFear/2 {
		t.Fatalf("got dragged dangerously close, worst edge distance %v", worstEdge)
	}
}

func TestChasesWhenNoBlackHoleNear(t *testing.T) {
	f := &fakeSender{}
	s := newSim(f)
	theShip(s).X, theShip(s).Y = 5000, 0
	players := map[string]PlayerData{"p1": {SocketId: "p1", ShipId: "s1", X: 0, Y: 0}}
	s.tickEnemyShip(theShip(s), players, time.Now())
	if theShip(s).X >= 5000 {
		t.Fatalf("should close in on the player, x=%v", theShip(s).X)
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
