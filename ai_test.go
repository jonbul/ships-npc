package main

import (
	"math"
	"testing"
	"time"
)

// The weights shipped in the binary must actually load: if they don't, the
// AI fleet silently degrades to the rule controller and the whole feature
// looks like it works while doing nothing.
func TestEmbeddedPolicyLoads(t *testing.T) {
	policy := loadAiPolicy()
	if policy == nil {
		t.Fatal("embedded aiPolicy.json did not load; run SHIPS_NPC_TRAIN=1 go test -run TestTrainAiPolicy")
	}
	if !policy.valid() {
		t.Fatal("embedded policy has the wrong shape")
	}
}

// Whatever it is fed, the policy has to return something the actuator can
// use. A NaN would propagate into the ship's rotation and put it off the
// map permanently, and it would only show up in production.
func TestPolicyOutputsStayInRange(t *testing.T) {
	policy := loadAiPolicy()
	if policy == nil {
		t.Skip("no policy")
	}
	for _, extreme := range []float64{0, 1, -1, 1e6, -1e6} {
		in := make([]float64, aiInputs)
		for i := range in {
			in[i] = extreme
		}
		command := policy.decide(in)
		if math.IsNaN(command.turn) || math.IsNaN(command.thrust) {
			t.Fatalf("policy produced NaN for input %v", extreme)
		}
		if math.Abs(command.turn) > 1 || math.Abs(command.thrust) > 1 {
			t.Fatalf("policy exceeded the actuator range: %+v", command)
		}
	}
}

// aiSim is newSim with the seeded ship flown by the learned policy.
func aiSim(f *fakeSender) *npcSimulator {
	s := newSim(f)
	settings := s.settings
	settings.EnemyShipController = controllerBoth
	settings.AiAttacksPlayers = true
	s.settings = settings
	theShip(s).controller = controllerAi
	return s
}

// The point of the whole exercise: a ship flown by the network has to
// actually hunt. It starts pointing the wrong way, so this also proves it
// turns the short way round rather than spinning.
func TestAiShipTurnsTowardsAPlayerAndFires(t *testing.T) {
	f := &fakeSender{}
	s := aiSim(f)
	if s.policy == nil {
		t.Skip("no policy")
	}
	ship := theShip(s)
	ship.X, ship.Y = 0, 0
	ship.Rotate = float32(math.Pi) // facing due west, away from the target

	players := map[string]PlayerData{"p1": {
		SocketId: "p1", ShipId: "s1", X: 600, Y: 0, Width: 100, Height: 200, Scale: 1,
	}}

	// A ship turns at the same fixed rate a player does (SPEED.ROTATION),
	// so half a turn alone takes ~160 ticks: this is a slow manoeuvre, not
	// a snap to the bearing.
	// Judged on the best aim of the run rather than the aim at some fixed
	// tick: like the rule fleet it copies, an AI ship makes attack runs
	// instead of parking in front of its target, so where it points at the
	// end depends on where it is in that cycle.
	now := time.Now()
	off := math.Pi
	for i := 0; i < 300; i++ {
		now = now.Add(100 * time.Millisecond)
		s.tickEnemyShip(ship, players, now)

		centerX, centerY := ship.center()
		bearing := math.Atan2(0+100-centerY, 600+50-centerX)
		off = math.Min(off, math.Abs(angleDifference(float64(ship.Rotate), bearing)))
	}

	if off > 0.4 {
		t.Fatalf("AI ship never lined up on the player: %.2f rad off", off)
	}
	if len(f.bullets) == 0 {
		t.Fatal("AI ship never fired")
	}
}

// The AI must be held to exactly the same envelope as the rule fleet: it
// decides, it does not act. If a policy ever asks for more than one tick's
// worth of turn or thrust, applyCommand has to clamp it.
func TestAiCannotExceedThePlayerEnvelope(t *testing.T) {
	f := &fakeSender{}
	s := aiSim(f)
	ship := theShip(s)
	ship.Rotate = 0
	ship.Speed = 0

	// A deliberately impossible command, as a broken or hostile policy
	// would produce.
	s.applyCommand(ship, shipCommand{turn: 500, thrust: 500}, time.Now())

	maxTurn := clientSpeedRotation * s.frameScale
	// float32 on the wire, so compare with a tolerance rather than exactly.
	if float64(ship.Rotate) > maxTurn+1e-6 {
		t.Fatalf("turned %v in one tick, more than the %v a player can", ship.Rotate, maxTurn)
	}
	if ship.Speed > s.maxSpeed+1e-9 {
		t.Fatalf("reached %v, faster than the configured max %v", ship.Speed, s.maxSpeed)
	}
}

// Both brains must fly on identical physics, so a fair comparison is
// possible at all: the same command applied to the same ship must produce
// the same movement whichever controller produced it.
func TestBothControllersShareTheSamePhysics(t *testing.T) {
	command := shipCommand{turn: 0.5, thrust: 1, fire: false}
	var results [2]enemyShipState

	for i, controller := range []controllerKind{controllerRule, controllerAi} {
		s := newSim(&fakeSender{})
		ship := theShip(s)
		ship.controller = controller
		ship.X, ship.Y, ship.Rotate, ship.Speed = 10, 20, 0.3, 5
		s.applyCommand(ship, command, time.Now())
		results[i] = *ship
	}

	if results[0].X != results[1].X || results[0].Y != results[1].Y ||
		results[0].Rotate != results[1].Rotate || results[0].Speed != results[1].Speed {
		t.Fatalf("the two controllers moved differently on the same command: %+v vs %+v", results[0], results[1])
	}
}

// A ship whose brain is missing must still fly: an unusable policy falls
// back to the rule controller, and has to do so exactly - the fallback
// ship's whole trajectory must match a rule ship's, not merely move.
func TestAiShipFallsBackToRulesWithoutAPolicy(t *testing.T) {
	players := map[string]PlayerData{"p1": {
		SocketId: "p1", ShipId: "s1", X: 600, Y: 0, Width: 100, Height: 200, Scale: 1,
	}}

	fly := func(controller controllerKind, policy *aiPolicy) (enemyShipState, int) {
		f := &fakeSender{}
		s := aiSim(f)
		s.policy = policy
		ship := theShip(s)
		ship.controller = controller
		ship.X, ship.Y, ship.Rotate = 0, 0, float32(math.Pi)
		// The per-ship flying jitter is rolled at random on the first
		// decision, so it has to be pinned for the two runs to be
		// comparable at all.
		ship.weavePhase, ship.weaveRate, ship.phaseSkew = 0, 1, 0

		now := time.Now()
		for i := 0; i < 200; i++ {
			now = now.Add(100 * time.Millisecond)
			s.tickEnemyShip(ship, players, now)
		}
		return *ship, len(f.bullets)
	}

	fallback, fallbackShots := fly(controllerAi, nil)
	rules, ruleShots := fly(controllerRule, loadAiPolicy())

	if fallback.X != rules.X || fallback.Y != rules.Y || fallback.Rotate != rules.Rotate {
		t.Fatalf("policy-less AI ship did not fly like a rule ship: %+v vs %+v", fallback, rules)
	}
	if fallbackShots != ruleShots {
		t.Fatalf("policy-less AI ship shot %d times, a rule ship %d", fallbackShots, ruleShots)
	}
}
