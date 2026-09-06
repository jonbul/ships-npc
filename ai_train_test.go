package main

import (
	"encoding/json"
	"math"
	"math/rand"
	"os"
	"strconv"
	"testing"
	"time"
)

// TestTrainAiPolicy trains the network in ai.go and writes aiPolicy.json,
// which is then embedded in the binary. It is a test rather than a
// `cmd/` program only because the trainer needs the simulator's
// unexported internals, and this service is a single `package main`.
//
// Run it deliberately, not on every `go test`:
//
//	SHIPS_NPC_TRAIN=1 go test -run TestTrainAiPolicy -timeout 30m
//
// The teacher is the existing rule controller. That is behaviour cloning:
// it needs no reward design and no exploration, and it converges in
// seconds on a laptop, where reinforcement learning against a live game
// would need hours and a reward function nobody can agree on. The ceiling
// is the teacher's own skill - the point of this fleet is to *compare* a
// learned controller against the hand-written one on identical physics,
// and to have the machinery in place, not to be superhuman on day one.
func TestTrainAiPolicy(t *testing.T) {
	if os.Getenv("SHIPS_NPC_TRAIN") != "1" {
		t.Skip("set SHIPS_NPC_TRAIN=1 to retrain the AI policy and rewrite aiPolicy.json")
	}

	rng := rand.New(rand.NewSource(7))
	samples := collectSamples(t, rng, 4000)
	t.Logf("collected %d samples", len(samples))
	if len(samples) < 5000 {
		t.Fatalf("not enough training data: %d samples", len(samples))
	}

	rng.Shuffle(len(samples), func(i, j int) { samples[i], samples[j] = samples[j], samples[i] })
	split := len(samples) * 4 / 5
	train, validate := samples[:split], samples[split:]

	policy := trainPolicy(t, rng, train, validate, 40)
	report(t, "final", policy, validate)

	encoded, err := json.Marshal(policy)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile("aiPolicy.json", encoded, 0o644); err != nil {
		t.Fatal(err)
	}
	t.Logf("wrote aiPolicy.json (%d bytes)", len(encoded))
}

// sample is one (situation, what the rule controller did) pair.
type sample struct {
	in  []float64
	out [3]float64 // turn, thrust, fire
}

// collectSamples runs real fights and records what the rule controller
// decided. The data comes from the simulator itself, through the same
// features() the policy uses at runtime, so there is no chance of the
// training inputs drifting from the serving inputs.
//
// Variety matters more than volume here: several fleet sizes, black holes
// on and off, ships hunting players and ships hunting each other, so the
// policy sees close range and long range, crowded and empty.
func collectSamples(t *testing.T, rng *rand.Rand, ticksPerScenario int) []sample {
	t.Helper()

	scenarios := []struct {
		ships         int
		players       int
		blackHoles    int
		npcInfighting bool
	}{
		{ships: 1, players: 1, blackHoles: 0},
		{ships: 3, players: 2, blackHoles: 0},
		{ships: 6, players: 1, blackHoles: 3},
		{ships: 8, players: 3, blackHoles: 2, npcInfighting: true},
		{ships: 4, players: 1, blackHoles: 4, npcInfighting: true},
	}

	var samples []sample
	for _, scenario := range scenarios {
		f := &fakeSender{}
		settings := defaultNpcSettings()
		settings.EnemyShips = scenario.ships
		settings.MaxBlackHoles = scenario.blackHoles
		settings.BlackHoleSpawnPeriodSec = 1
		settings.NpcAttacksNpc = scenario.npcInfighting
		s := settingsSim(f, settings)
		s.observer = func(view shipView, command shipCommand) {
			fire := 0.0
			if command.fire {
				fire = 1
			}
			samples = append(samples, sample{
				in:  s.features(view, view.fireReady),
				out: [3]float64{command.turn, command.thrust, fire},
			})
		}

		players := map[string]PlayerData{}
		for i := 0; i < scenario.players; i++ {
			id := string(rune('a' + i))
			players[id] = PlayerData{
				SocketId: id, ShipId: "s1",
				X:     float32(rng.Float64()*4000 - 2000),
				Y:     float32(rng.Float64()*4000 - 2000),
				Width: 100, Height: 200, Scale: 1,
			}
		}

		now := time.Now()
		for i := 0; i < ticksPerScenario; i++ {
			now = now.Add(100 * time.Millisecond)
			// Players wander, so the ships face a moving target rather
			// than learning to point at one fixed spot.
			for id, p := range players {
				p.X += float32(rng.NormFloat64() * 12)
				p.Y += float32(rng.NormFloat64() * 12)
				players[id] = p
			}
			s.mu.Lock()
			if scenario.blackHoles > 0 && len(s.npcs) < scenario.blackHoles {
				bh := spawnBlackHole(players, now, defaultBlackHoleDurationSec*time.Second)
				s.npcs[bh.Id] = bh
			}
			s.tickEnemyShips(players, now)
			s.advanceBullets(now)
			s.mu.Unlock()
		}
	}
	return samples
}

// trainPolicy is plain minibatch gradient descent with momentum. Written
// by hand because the whole network is three small matrices: pulling in a
// deep learning framework (and cgo, and a 100 MB dependency) to multiply
// them would be absurd.
func trainPolicy(t *testing.T, rng *rand.Rand, train, validate []sample, epochs int) *aiPolicy {
	t.Helper()

	policy := newRandomPolicy(rng)
	velocity := zerosLike(policy)

	// Firing is rare - a ship spends most of its time turning towards
	// something - so an unweighted loss is minimised by never firing at
	// all. Weighting the positive class by its own rarity keeps the two
	// worth the same in aggregate.
	fires := 0.0
	for _, s := range train {
		fires += s.out[2]
	}
	fireWeight := 1.0
	if fires > 0 {
		fireWeight = (float64(len(train)) - fires) / fires
	}
	t.Logf("firing in %.1f%% of samples, positive weight %.2f", 100*fires/float64(len(train)), fireWeight)

	const (
		batchSize = 64
		momentum  = 0.9
	)
	learningRate := 0.02

	for epoch := 0; epoch < epochs; epoch++ {
		rng.Shuffle(len(train), func(i, j int) { train[i], train[j] = train[j], train[i] })
		for start := 0; start < len(train); start += batchSize {
			end := start + batchSize
			if end > len(train) {
				end = len(train)
			}
			batch := train[start:end]
			grad := zerosLike(policy)
			for _, s := range batch {
				accumulateGradient(policy, grad, s, fireWeight)
			}
			scale := learningRate / float64(len(batch))
			applyGradient(policy, velocity, grad, scale, momentum)
		}
		// A gentle decay: the first epochs need to move fast, the last
		// ones need to stop bouncing around the minimum.
		learningRate *= 0.95
		if epoch%10 == 9 {
			report(t, "epoch "+strconv.Itoa(epoch+1), policy, validate)
		}
	}
	return policy
}

// accumulateGradient does one forward and one backward pass, adding this
// sample's gradient into grad.
func accumulateGradient(p, grad *aiPolicy, s sample, fireWeight float64) {
	h1 := layer(p.W1, p.B1, s.in, math.Tanh)
	h2 := layer(p.W2, p.B2, h1, math.Tanh)
	out := layer(p.W3, p.B3, h2, nil)

	turn := math.Tanh(out[0])
	thrust := math.Tanh(out[1])
	fire := sigmoid(out[2])

	// dLoss/d(pre-activation) for each output. Squared error through tanh
	// for the two continuous controls; cross-entropy through sigmoid for
	// firing, where the sigmoid derivative cancels and leaves (y - target).
	weight := 1.0
	if s.out[2] > 0.5 {
		weight = fireWeight
	}
	delta3 := []float64{
		(turn - s.out[0]) * (1 - turn*turn),
		(thrust - s.out[1]) * (1 - thrust*thrust),
		(fire - s.out[2]) * weight,
	}

	delta2 := backward(p.W3, delta3, h2)
	delta1 := backward(p.W2, delta2, h1)

	addOuter(grad.W3, grad.B3, delta3, h2)
	addOuter(grad.W2, grad.B2, delta2, h1)
	addOuter(grad.W1, grad.B1, delta1, s.in)
}

// backward propagates a layer's deltas back through its weights and the
// tanh of the layer below (whose output is `below`).
func backward(weights [][]float64, delta, below []float64) []float64 {
	out := make([]float64, len(below))
	for j := range below {
		sum := 0.0
		for i, row := range weights {
			sum += row[j] * delta[i]
		}
		out[j] = sum * (1 - below[j]*below[j])
	}
	return out
}

func addOuter(weights [][]float64, biases, delta, in []float64) {
	for i, d := range delta {
		biases[i] += d
		row := weights[i]
		for j, v := range in {
			row[j] += d * v
		}
	}
}

func applyGradient(p, velocity, grad *aiPolicy, scale, momentum float64) {
	step := func(w, v, g [][]float64, wb, vb, gb []float64) {
		for i := range w {
			for j := range w[i] {
				v[i][j] = momentum*v[i][j] - scale*g[i][j]
				w[i][j] += v[i][j]
			}
			vb[i] = momentum*vb[i] - scale*gb[i]
			wb[i] += vb[i]
		}
	}
	step(p.W1, velocity.W1, grad.W1, p.B1, velocity.B1, grad.B1)
	step(p.W2, velocity.W2, grad.W2, p.B2, velocity.B2, grad.B2)
	step(p.W3, velocity.W3, grad.W3, p.B3, velocity.B3, grad.B3)
}

// report prints how closely the policy reproduces the teacher, which is
// the only meaningful measure of a behaviour-cloned controller.
func report(t *testing.T, label string, p *aiPolicy, validate []sample) {
	t.Helper()

	turnError, thrustAgree, fireAgree := 0.0, 0, 0
	for _, s := range validate {
		command := p.decide(s.in)
		turnError += math.Abs(command.turn - s.out[0])
		if (command.thrust >= 0) == (s.out[1] >= 0) {
			thrustAgree++
		}
		if command.fire == (s.out[2] > 0.5) {
			fireAgree++
		}
	}
	n := float64(len(validate))
	t.Logf("%s: turn MAE %.4f, thrust agreement %.1f%%, fire agreement %.1f%%",
		label, turnError/n, 100*float64(thrustAgree)/n, 100*float64(fireAgree)/n)
}

// newRandomPolicy initialises with Xavier scaling, which keeps the
// activations away from tanh's flat tails where gradients vanish.
func newRandomPolicy(rng *rand.Rand) *aiPolicy {
	randomMatrix := func(rows, cols int) [][]float64 {
		limit := math.Sqrt(6.0 / float64(rows+cols))
		m := make([][]float64, rows)
		for i := range m {
			m[i] = make([]float64, cols)
			for j := range m[i] {
				m[i][j] = (rng.Float64()*2 - 1) * limit
			}
		}
		return m
	}
	return &aiPolicy{
		W1: randomMatrix(aiHidden, aiInputs), B1: make([]float64, aiHidden),
		W2: randomMatrix(aiHidden, aiHidden), B2: make([]float64, aiHidden),
		W3: randomMatrix(aiOutputs, aiHidden), B3: make([]float64, aiOutputs),
	}
}

func zerosLike(p *aiPolicy) *aiPolicy {
	zeros := func(like [][]float64) [][]float64 {
		m := make([][]float64, len(like))
		for i := range like {
			m[i] = make([]float64, len(like[i]))
		}
		return m
	}
	return &aiPolicy{
		W1: zeros(p.W1), B1: make([]float64, len(p.B1)),
		W2: zeros(p.W2), B2: make([]float64, len(p.B2)),
		W3: zeros(p.W3), B3: make([]float64, len(p.B3)),
	}
}
