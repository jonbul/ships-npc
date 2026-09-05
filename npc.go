package main

import (
	"math"
	"math/rand"
	"sync"
	"time"

	"github.com/google/uuid"
)

const (
	maxActiveBlackHoles  = 10
	blackHoleSpawnPeriod = 30 * time.Second
	blackHoleDuration    = 180 * time.Second
	blackHoleSpawnMargin = 2000
	blackHoleScaleStep   = 0.01
	blackHoleSpeed       = 7.5
)

// npcSimulator owns and simulates every NPC (black holes today, more kinds
// in the future). It is the single source of truth for NPC state: ships-go
// no longer simulates anything, it only relays whatever this service sends.
type npcSimulator struct {
	mu        sync.Mutex
	npcs      map[string]*npcState
	lastSpawn time.Time
}

type npcState struct {
	NpcData
	spawnedAt time.Time
}

func newNpcSimulator() *npcSimulator {
	return &npcSimulator{npcs: make(map[string]*npcState)}
}

// tick advances every NPC's simulation by one step and, when conditions are
// met, spawns a new black hole near the current players. players is a
// snapshot of currently known player positions (see playerTracker).
func (s *npcSimulator) tick(players map[string]PlayerData) {
	s.mu.Lock()
	defer s.mu.Unlock()

	now := time.Now()

	if len(players) > 1 && len(s.npcs) < maxActiveBlackHoles && now.Sub(s.lastSpawn) > blackHoleSpawnPeriod {
		bh := spawnBlackHole(players, now)
		s.npcs[bh.Id] = bh
		s.lastSpawn = now
	}

	toRemove := make([]string, 0)
	for id, npc := range s.npcs {
		inTime := now.Sub(npc.spawnedAt) < blackHoleDuration

		switch {
		case inTime && npc.Scale < 1.0:
			npc.Scale += blackHoleScaleStep
		case !inTime && npc.Scale <= blackHoleScaleStep:
			toRemove = append(toRemove, id)
			continue
		case !inTime && npc.Scale > blackHoleScaleStep:
			npc.Scale -= blackHoleScaleStep
		}

		radians := npc.Direction * (math.Pi / 180)
		npc.X += npc.Speed * math.Cos(radians)
		npc.Y += npc.Speed * math.Sin(radians)
	}

	for _, id := range toRemove {
		delete(s.npcs, id)
	}
}

// snapshot returns the full current NPC batch, ready to be sent to
// ships-go in a single npcUpdate message.
func (s *npcSimulator) snapshot() []NpcData {
	s.mu.Lock()
	defer s.mu.Unlock()

	out := make([]NpcData, 0, len(s.npcs))
	for _, npc := range s.npcs {
		out = append(out, npc.NpcData)
	}
	return out
}

func spawnBlackHole(players map[string]PlayerData, now time.Time) *npcState {
	var minX, minY, maxX, maxY float32
	first := true
	for _, p := range players {
		if first {
			minX, minY, maxX, maxY = p.X, p.Y, p.X, p.Y
			first = false
			continue
		}
		minX = min(minX, p.X)
		minY = min(minY, p.Y)
		maxX = max(maxX, p.X)
		maxY = max(maxY, p.Y)
	}

	minX -= blackHoleSpawnMargin
	maxX += blackHoleSpawnMargin
	minY -= blackHoleSpawnMargin
	maxY += blackHoleSpawnMargin

	rangeX := maxX - minX
	rangeY := maxY - minY

	return &npcState{
		spawnedAt: now,
		NpcData: NpcData{
			Type:      NpcTypes.BlackHole,
			Id:        uuid.NewString(),
			X:         rand.Float64()*float64(rangeX) + float64(minX),
			Y:         rand.Float64()*float64(rangeY) + float64(minY),
			Scale:     blackHoleScaleStep,
			MaxSize:   800,
			Direction: rand.Float64() * 360,
			Duration:  int(blackHoleDuration.Milliseconds()),
			Speed:     blackHoleSpeed,
		},
	}
}
