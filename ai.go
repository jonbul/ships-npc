package main

import (
	_ "embed"
	"encoding/json"
	"log"
	"math"
)

// This file is the "AI" fleet's brain: a tiny multi-layer perceptron that
// runs entirely inside this process, on the CPU, with no model server, no
// cgo and no network call. It is deliberately small enough that inference
// is cheaper than the geometry the simulator already does per ship, which
// is what makes a hundred of them affordable at the tick rate.
//
// Why not something bigger: a ship's decision is a reflex from a handful
// of numbers (where the target is, how fast we are going, is the gun
// ready), taken 10 times a second per ship. That is a control problem, not
// a reasoning one - a language model would be several orders of magnitude
// too slow and too large for it, and would still have to be reduced to
// these same three outputs.
//
// The policy is trained offline by TestTrainAiPolicy (see ai_train_test.go)
// and its weights are embedded in the binary, so a deployment is still a
// single static file with nothing to download at startup.

const (
	aiInputs  = 16
	aiHidden  = 32
	aiOutputs = 3
)

//go:embed aiPolicy.json
var embeddedPolicy []byte

// aiPolicy is a 16 -> 32 -> 32 -> 3 network, about 1.5k parameters. The
// hidden layers use tanh (bounded, so a surprising input cannot produce a
// huge activation and a wild manoeuvre); the outputs are turn and thrust
// through tanh, giving the [-1,1] the actuator expects by construction,
// and fire through a sigmoid read as a probability.
type aiPolicy struct {
	W1 [][]float64 `json:"w1"`
	B1 []float64   `json:"b1"`
	W2 [][]float64 `json:"w2"`
	B2 []float64   `json:"b2"`
	W3 [][]float64 `json:"w3"`
	B3 []float64   `json:"b3"`
}

// loadAiPolicy reads the embedded weights. A missing or malformed policy
// is not fatal: the AI fleet falls back to the rule controller, because an
// untrained network would fly the ships into walls and an admin switching
// to "ai" would just see the game break with no explanation.
func loadAiPolicy() *aiPolicy {
	policy := &aiPolicy{}
	if err := json.Unmarshal(embeddedPolicy, policy); err != nil {
		log.Println("ships-npc: AI policy unreadable, AI ships will fly on the rule controller:", err)
		return nil
	}
	if !policy.valid() {
		log.Println("ships-npc: AI policy has the wrong shape, AI ships will fly on the rule controller")
		return nil
	}
	return policy
}

func (p *aiPolicy) valid() bool {
	if len(p.W1) != aiHidden || len(p.B1) != aiHidden ||
		len(p.W2) != aiHidden || len(p.B2) != aiHidden ||
		len(p.W3) != aiOutputs || len(p.B3) != aiOutputs {
		return false
	}
	for _, row := range p.W1 {
		if len(row) != aiInputs {
			return false
		}
	}
	for _, row := range p.W2 {
		if len(row) != aiHidden {
			return false
		}
	}
	for _, row := range p.W3 {
		if len(row) != aiHidden {
			return false
		}
	}
	return true
}

// decide runs one forward pass and turns the outputs into the same
// shipCommand the rule controller emits, so applyCommand cannot tell them
// apart and the AI is held to the identical turn rate, acceleration and
// fire cooldown.
func (p *aiPolicy) decide(features []float64) shipCommand {
	h1 := layer(p.W1, p.B1, features, math.Tanh)
	h2 := layer(p.W2, p.B2, h1, math.Tanh)
	out := layer(p.W3, p.B3, h2, nil)

	return shipCommand{
		turn:   math.Tanh(out[0]),
		thrust: math.Tanh(out[1]),
		fire:   sigmoid(out[2]) > 0.5,
	}
}

// layer computes activation(W*in + b). Written out rather than pulled from
// a matrix library: at these sizes the whole forward pass is ~1.5k
// multiply-adds, and a dependency would cost more than it saves.
func layer(weights [][]float64, biases, in []float64, activation func(float64) float64) []float64 {
	out := make([]float64, len(weights))
	for i, row := range weights {
		sum := biases[i]
		for j, w := range row {
			sum += w * in[j]
		}
		if activation != nil {
			sum = activation(sum)
		}
		out[i] = sum
	}
	return out
}

func sigmoid(x float64) float64 { return 1 / (1 + math.Exp(-x)) }

// features turns a shipView into the network's input vector. Everything is
// relative to the ship itself and roughly bounded to [-1,1]: a policy fed
// absolute map coordinates would only work in the part of the map it was
// trained in, and unbounded inputs make training unstable.
//
// Angles are given as a (sin, cos) pair rather than a single number
// because an angle wraps: -179 degrees and +179 degrees are almost the
// same heading but the furthest apart values, which a network cannot
// learn around.
func (s *npcSimulator) features(view shipView, fireReady bool) []float64 {
	rotate := float64(view.self.Rotate)
	toTarget := angleDifference(rotate, view.bearing)
	toDesired := angleDifference(rotate, view.desired)

	maxSpeed := s.maxSpeed
	if maxSpeed <= 0 {
		maxSpeed = 1
	}

	bhSin, bhCos, bhCloseness := 0.0, 0.0, 0.0
	if view.bhFound {
		escape := math.Atan2(view.y-view.bhY, view.x-view.bhX)
		toEscape := angleDifference(rotate, escape)
		bhSin, bhCos = math.Sin(toEscape), math.Cos(toEscape)
		// Closeness rather than distance, so "no black hole anywhere" and
		// "a black hole very far away" are the same input (0) instead of
		// the two extremes of the range.
		bhCloseness = 1 - math.Min(view.bhEdge/enemyShipBlackHoleFear, 1)
	}

	// Where the nearest bullet on a collision course is, weighted by how
	// soon it arrives. Without these the AI fleet flies blind into fire:
	// it can only learn to get out of the way of something it is told
	// about, and it would keep aiming while it was shot.
	thSin, thCos, urgency := 0.0, 0.0, 0.0
	if view.threatFound {
		toThreat := angleDifference(rotate, view.threatAngle)
		thSin, thCos = math.Sin(toThreat), math.Cos(toThreat)
		urgency = view.threatUrgency
	}

	return []float64{
		math.Sin(toTarget),
		math.Cos(toTarget),
		math.Sin(toDesired),
		math.Cos(toDesired),
		math.Min(view.dist/enemyShipShootRange, 2),
		boolFeature(view.dist <= enemyShipStandoff),
		view.self.Speed / maxSpeed,
		math.Min(view.target.radius/enemyShipShootRange, 1),
		boolFeature(view.target.isShip),
		bhSin * bhCloseness,
		bhCos * bhCloseness,
		boolFeature(fireReady),
		thSin * urgency,
		thCos * urgency,
		urgency,
		// The ship's own place in the attack cycle, advanced by the
		// simulator for every ship whatever flies it. The manoeuvre being
		// copied has memory - it commits to a pass and to a break-off -
		// and a network shown only the geometry would have to average the
		// two halves together and fly neither.
		boolFeature(view.self.breakingAway),
	}
}

func boolFeature(value bool) float64 {
	if value {
		return 1
	}
	return 0
}
