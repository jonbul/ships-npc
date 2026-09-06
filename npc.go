package main

import (
	"log"
	"math"
	"math/rand"
	"sort"
	"sync"
	"time"

	"github.com/google/uuid"
)

const (
	blackHoleDuration    = 180 * time.Second
	blackHoleSpawnMargin = 2000
	blackHoleScaleStep   = 0.01
	blackHoleSpeed       = 7.5

	// Black hole gravity, mirrored 1:1 from ships-vue's applyNpcImpacts()
	// so an NPC ship is sucked in exactly like a player is. ships-vue
	// applies this once per fixed timestep, so the values are per client
	// frame and get rescaled to this service's tick rate (see frameScale).
	blackHolePullRange  = 2000.0 // edge distance at which gravity starts
	blackHolePullFactor = 1000.0
	blackHolePullBoost  = 1.25
	clientTickInterval  = time.Second / 30 // ships-vue's fixed timestep

	// Enemy ship: hostile Ship NPCs that chase the nearest player and
	// shoot at them. Any number can be simulated at once; how many is one
	// of the settings an admin can change live (see npcSettings).
	//
	// Its speed model is derived from ships-vue's SPEED constants
	// (public/js/utils/constants.js) rather than being invented here, so
	// the ship is actually comparable to a player: hardcoded values were
	// ~6x too slow, leaving it trailing forever at a near-constant bearing
	// and never reaching firing range. These are per *client frame*, so
	// they're rescaled to this service's tick rate (see frameScale).
	clientSpeedStep     = 0.2  // SPEED.STEP
	clientSpeedMax      = 50.0 // SPEED.MAX
	clientSpeedRotation = 0.02 // SPEED.ROTATION, radians per client frame

	// Cruising speed of an enemy ship, expressed in the game's own speed
	// units (the same scale as ships-vue's SPEED.MAX, i.e. px per client
	// frame) so an admin tunes it against a number that means something in
	// game terms rather than an abstract multiplier. Admin-tunable, so this
	// is only the default: at 20 against SPEED.MAX of 50, a ship cruises at
	// 40% of a player's top speed, so it's a real threat that a player at
	// full throttle can still outrun.
	defaultEnemyShipSpeed = 20.0
	enemyShipBrakeFactor  = 3.0 // brakes harder than it accelerates
	// Escaping a black hole is a full-throttle emergency, so it gets a
	// player's full top speed and a much punchier acceleration.
	enemyShipEscapeSpeedFactor = 1.0
	enemyShipEscapeAccelFactor = 3.0

	enemyShipStandoff = 500.0 // stop closing in once this close, to keep shooting range

	// A ship only fires when its nose is actually pointing at the target,
	// like a player has to. The tolerance is worked out per shot from how
	// wide the target looks at its current distance, but clamped: without a
	// floor a distant target would be impossible to line up on, and without
	// a ceiling a ship parked next to its target would fire wildly sideways.
	enemyShipAimToleranceMin = 0.02
	enemyShipAimToleranceMax = 0.35

	// Ships push each other apart on contact and steer away from crowding,
	// so a group hunting the same player spreads into a loose formation
	// instead of stacking into one pile. Separation is measured between
	// ship centers, scaled by their size.
	enemyShipSeparationFactor = 2.5 // desired gap as a multiple of ship radius
	enemyShipSeparationWeight = 1.4 // how strongly crowding bends the heading
	// Pushed slightly further than exactly touching: resolving to a gap of
	// precisely zero leaves ships in permanent contact, re-triggering the
	// push every tick (and leaving a floating point residue behind).
	enemyShipSeparationEpsilon = 0.01
	// How many times per tick the pairwise separation is re-run; see
	// separateEnemyShips. A pile of ships needs a handful; the loop stops as soon as nothing
	enemyShipSeparationPasses = 32
	// Edge distance to a black hole at which the ship gives up on its
	// target and runs for its life. Sits just inside blackHolePullRange so
	// it turns and runs while the pull is still weak enough to out-run.
	// It then keeps running until it's fully clear of the hole's gravity
	// (Safe > blackHolePullRange): without that hysteresis a ship chasing
	// a player who's sitting on a black hole just oscillates across the
	// threshold, never escaping and never attacking.
	enemyShipBlackHoleFear = 1800.0
	enemyShipBlackHoleSafe = 2500.0
	enemyShipShootRange    = 1400.0
	enemyShipRespawnDelay  = 8 * time.Second
	enemyShipSpawnMargin   = 1500
	enemyShipScale         = 1.0
	npcBulletShootingSpeed = 0 // matches ships-vue's default (no charge bonus)
	npcBulletSpeed         = 25*1.5 + npcBulletShootingSpeed
	npcBulletRange         = 5000
	npcBulletCharge        = 3 // damage per hit; player life defaults to 10
	npcBulletLifetime      = 3 * time.Second
)

// npcSimulator owns and simulates every NPC (black holes, the enemy ship,
// more kinds in the future). It is the single source of truth for NPC
// state: ships-go no longer simulates anything, it only relays whatever
// this service sends.
type npcSimulator struct {
	mu        sync.Mutex
	npcs      map[string]*npcState
	lastSpawn time.Time

	// Enemy ships (destructible, chase + shoot the nearest player).
	sender    bulletSender
	ships     []publicShip
	shipsById map[string]publicShip // to resolve a target player's ship size
	// fallbackShip is the ship used for a player whose shipId isn't in the
	// public list - see shipFor().
	fallbackShip    publicShip
	hasFallbackShip bool
	enemyShips      map[string]*enemyShipState
	enemyRespawnAt  time.Time
	// retiredShips holds the identities of ships that have been destroyed
	// (or despawned when the fleet shrank), newest last. Respawning reuses
	// one instead of inventing a new uuid/name, so an NPC keeps its name
	// and its kill/death record across deaths - which is what makes it
	// worth listing in ships-vue's scoreboard at all, and reads like a
	// recurring rival rather than an endless parade of strangers.
	retiredShips []*enemyShipState
	// activeBullets are the bullets this service has fired and not yet
	// retired. Their trajectory is kept (not just the time they were
	// fired) because NPC-vs-NPC hits have to be resolved here: no client
	// tracks them. Player-vs-NPC still arrives as an npcHit from the
	// shooting player's own client, and NPC-vs-player is still that
	// player's client's job, exactly as before.
	activeBullets map[string]*npcBullet

	// Live admin-tunable settings, replaced wholesale by applySettings
	// whenever ships-go pushes an npcConfig event. Read under mu like
	// everything else here, since the push arrives on the client's read
	// goroutine, not the tick loop.
	settings npcSettings

	// How many ships-vue frames one tick of this service represents. Both
	// the black hole pull and the ship's speed model are expressed in the
	// client's per-frame units, so they're rescaled by this to stay
	// equivalent in wall-clock terms whatever NPC_TICK_INTERVAL_MS is.
	frameScale float64

	// Enemy ship speed envelope, in px per tick, derived from frameScale.
	// maxSpeed also depends on the admin-tunable speed factor, so it's
	// recomputed by applySettings rather than fixed at construction.
	maxSpeed       float64
	accel          float64
	decel          float64
	escapeMaxSpeed float64
	escapeAccel    float64
}

type npcState struct {
	NpcData
	spawnedAt time.Time
}

// npcBullet is one in-flight bullet fired by an enemy ship, flown here in
// the same straight line ships-vue's Bullet class flies it in (constant
// velocity along its firing angle), so what this service thinks it hit
// matches what players see.
type npcBullet struct {
	ownerId      string
	x, y         float64
	stepX, stepY float64 // px per tick, already scaled by frameScale
	firedAt      time.Time
}

// enemyShipState is the enemy ship's own npcState, kept separate from
// npcState (black holes) since it carries extra fields (life, target).
type enemyShipState struct {
	NpcData
	ship       publicShip // needed to fire bullets from the ship's actual center
	spawnedAt  time.Time
	fleeing    bool      // escaping a black hole; see enemyShipBlackHoleSafe
	lastShotAt time.Time // per ship, so several of them don't fire in lockstep
}

// radius is the ship's collision radius, resolved exactly like ships-vue
// does for any drawable it tests against a black hole: the half-diagonal
// of its bounding box (getRadiusFromRect).
func (e *enemyShipState) center() (float64, float64) {
	offsetX, offsetY := e.ship.centerOffset()
	return e.X + offsetX, e.Y + offsetY
}

func (e *enemyShipState) radius() float64 {
	offsetX, offsetY := e.ship.centerOffset()
	return math.Hypot(offsetX*e.Scale, offsetY*e.Scale)
}

// bulletSender is the subset of npcClient the simulator needs to fire
// bullets and announce kills, without depending on npcClient directly
// (keeps npc.go testable/independent of the transport).
type bulletSender interface {
	sendBullet(newBulletMsg) error
	sendRemoveBullet(bulletId string) error
	sendPlayerDied(playerDiedMsg) error
}

func newNpcSimulator(sender bulletSender, ships []publicShip, tickInterval time.Duration) *npcSimulator {
	// ships-vue resolves every player's ship from this same public list
	// (ShipsManager.getShipById, fed by GET /game/getShips), so indexing it
	// here is enough to know any player's ship dimensions.
	shipsById := make(map[string]publicShip, len(ships))
	for _, ship := range ships {
		shipsById[ship.Id] = ship
	}
	fallbackShip, hasFallbackShip := pickFallbackShip(ships)
	frameScale := float64(tickInterval) / float64(clientTickInterval)
	if frameScale <= 0 {
		frameScale = 1
	}
	// A speed in px/frame becomes px/tick by multiplying by frameScale; an
	// acceleration is per frame *per frame*, so it scales quadratically.
	accel := clientSpeedStep * frameScale * frameScale
	sim := &npcSimulator{
		npcs:            make(map[string]*npcState),
		sender:          sender,
		ships:           ships,
		shipsById:       shipsById,
		fallbackShip:    fallbackShip,
		hasFallbackShip: hasFallbackShip,
		enemyShips:      make(map[string]*enemyShipState),
		activeBullets:   make(map[string]*npcBullet),
		frameScale:      frameScale,
		accel:           accel,
		decel:           accel * enemyShipBrakeFactor,
		escapeMaxSpeed:  clientSpeedMax * enemyShipEscapeSpeedFactor * frameScale,
		escapeAccel:     accel * enemyShipEscapeAccelFactor,
	}
	sim.applySettings(defaultNpcSettings())
	return sim
}

// applySettings swaps in a new set of admin-chosen settings. It's called
// from the client's read goroutine when ships-go pushes an npcConfig, so
// it takes the same lock the tick loop does; everything it changes is read
// fresh on the next tick, which is what makes the panel feel immediate.
func (s *npcSimulator) applySettings(settings npcSettings) {
	s.mu.Lock()
	defer s.mu.Unlock()

	s.settings = settings.sanitized()
	s.maxSpeed = s.settings.EnemyShipSpeed * s.frameScale
}

// currentSettings returns a copy of the settings in force, for logging.
func (s *npcSimulator) currentSettings() npcSettings {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.settings
}

// pickFallbackShip mirrors ships-vue's ShipsManager.getShipById /
// gameStatus.getShipById fallback: the first ship that isn't somebody's
// personal project, else simply the first one.
func pickFallbackShip(ships []publicShip) (publicShip, bool) {
	for _, ship := range ships {
		if ship.UserId == "" {
			return ship, true
		}
	}
	if len(ships) > 0 {
		return ships[0], true
	}
	return publicShip{}, false
}

// shipFor resolves the ship a player is flying, as every browser resolves
// it. GET /game/getShips only lists the *public* ships, so a player flying
// one of their own painting projects has a shipId that is absent from it.
// ships-vue already handles that by rendering them with a fallback ship,
// so that fallback is the ship actually on screen - and therefore the one
// whose geometry this service has to aim at. Falling back to a zero offset
// instead (aiming at the top-left anchor, radius 50) put every shot half a
// ship off, which up close is tens of degrees of aiming error and looks
// exactly like the NPC turning to the wrong place.
func (s *npcSimulator) shipFor(shipId string) (publicShip, bool) {
	if ship, ok := s.shipsById[shipId]; ok {
		return ship, true
	}
	return s.fallbackShip, s.hasFallbackShip
}

// playerSize returns the player's raw, unscaled ship size and the factor it
// is drawn at. The client sends all three, which is the only way to know a
// player's geometry for certain: GET /game/getShips lists only the *public*
// ships, so a player flying one of their own painting projects cannot be
// looked up at all. The ship-list lookup is kept as a fallback for clients
// that predate those fields.
func (s *npcSimulator) playerSize(p PlayerData) (width, height, scale float64, ok bool) {
	scale = float64(p.Scale)
	if scale <= 0 {
		scale = 1
	}
	if p.Width > 0 && p.Height > 0 {
		return float64(p.Width), float64(p.Height), scale, true
	}
	ship, found := s.shipFor(p.ShipId)
	if !found {
		return 0, 0, scale, false
	}
	w, h := ship.size()
	return w, h, scale, true
}

// playerCenter returns the middle of a player's ship. Player positions on
// the wire are the ship's top-left anchor, so aiming straight at them means
// aiming at a corner: harmless at long range, but a systematic miss up
// close, where being half a ship off is a large angular error.
//
// The center does not move with Scale: the client offsets a scaled ship by
// xTranslation = (width - width*scale)/2, so
// x + xTranslation + (width*scale)/2 == x + width/2 exactly.
func (s *npcSimulator) playerCenter(p PlayerData) (float64, float64) {
	width, height, _, ok := s.playerSize(p)
	if !ok {
		return float64(p.X), float64(p.Y)
	}
	return float64(p.X) + width/2, float64(p.Y) + height/2
}

// tick advances every NPC's simulation by one step: spawns/moves black
// holes as before, plus drives the enemy ship (spawn/respawn, chase the
// nearest player, shoot, expire its own bullets). players is a snapshot of
// currently known player positions (see playerTracker).
func (s *npcSimulator) tick(players map[string]PlayerData) {
	s.mu.Lock()
	defer s.mu.Unlock()

	now := time.Now()

	if len(players) > 1 && len(s.npcs) < s.settings.MaxBlackHoles && now.Sub(s.lastSpawn) > s.settings.blackHoleSpawnPeriod() {
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

	s.tickEnemyShips(players, now)
	s.advanceBullets(now)
}

// snapshot returns the full current NPC batch, ready to be sent to
// ships-go in a single npcUpdate message.
func (s *npcSimulator) snapshot() []NpcData {
	s.mu.Lock()
	defer s.mu.Unlock()

	out := make([]NpcData, 0, len(s.npcs)+len(s.enemyShips))
	for _, npc := range s.npcs {
		out = append(out, npc.NpcData)
	}
	for _, ship := range s.enemyShips {
		out = append(out, ship.NpcData)
	}
	return out
}

// tickEnemyShips keeps the fleet at the size the admin asked for, then
// advances each ship independently. Ships are simulated separately (own
// target, own throttle, own fire cooldown) rather than as a squadron: they
// end up spread out and attacking from different angles for free, and no
// per-ship decision can stall the others.
func (s *npcSimulator) tickEnemyShips(players map[string]PlayerData, now time.Time) {
	s.manageFleetSize(players, now)
	for _, ship := range s.enemyShips {
		s.tickEnemyShip(ship, players, now)
	}
	// Resolved after every ship has moved, so the pass sees final positions
	// for this tick rather than a mix of moved and not-yet-moved ships.
	s.separateEnemyShips()
}

// manageFleetSize spawns ships up to the configured count and despawns any
// excess after the count is lowered. A despawn is deliberately silent (no
// playerDied): nobody killed them, so an explosion and a kill-feed entry
// would be a lie. ships-vue drops any NPC missing from the batch, so
// leaving them out of the next snapshot is all that's needed.
func (s *npcSimulator) manageFleetSize(players map[string]PlayerData, now time.Time) {
	desired := s.settings.EnemyShips

	for id, ship := range s.enemyShips {
		if len(s.enemyShips) <= desired {
			break
		}
		s.retireEnemyShip(ship)
		delete(s.enemyShips, id)
	}

	if len(s.enemyShips) >= desired || len(players) == 0 || len(s.ships) == 0 {
		return
	}
	// The respawn delay is only armed by a death, so the initial fleet
	// (and any increase made from the admin panel) appears immediately.
	if now.Before(s.enemyRespawnAt) {
		return
	}
	for len(s.enemyShips) < desired {
		ship := s.reviveEnemyShip(players, now)
		s.enemyShips[ship.Id] = ship
	}
}

// retireEnemyShip parks a ship's identity so the next respawn can bring it
// back with the same id, name, hull and score.
func (s *npcSimulator) retireEnemyShip(enemyShip *enemyShipState) {
	s.retiredShips = append(s.retiredShips, enemyShip)
}

// reviveEnemyShip brings back the ship that has been out of the game
// longest, resetting only what a new life resets (position, health,
// heading, throttle) and keeping its identity and score. It falls back to
// spawning a brand new ship when nothing has died yet.
func (s *npcSimulator) reviveEnemyShip(players map[string]PlayerData, now time.Time) *enemyShipState {
	if len(s.retiredShips) == 0 {
		return spawnEnemyShip(players, s.ships, now, s.settings.EnemyShipLife)
	}

	revived := s.retiredShips[0]
	s.retiredShips = s.retiredShips[1:]

	fresh := spawnEnemyShip(players, s.ships, now, s.settings.EnemyShipLife)
	revived.spawnedAt = now
	revived.fleeing = false
	revived.lastShotAt = time.Time{}
	revived.X = fresh.X
	revived.Y = fresh.Y
	revived.Rotate = fresh.Rotate
	revived.Speed = 0
	revived.Scale = enemyShipScale
	revived.Life = s.settings.EnemyShipLife
	revived.MaxLife = s.settings.EnemyShipLife
	return revived
}

// recordKills credits an enemy ship for every player it killed. The hit is
// always detected by the victim's own client, so the only way this service
// learns about it is ships-go relaying the playerDied event back to us.
// Kills where the victim is one of our own ships are ignored: that death
// was announced by this service and already counted locally.
func (s *npcSimulator) recordKills(kills []killEventData) {
	if len(kills) == 0 {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()

	for _, kill := range kills {
		if _, isOurs := s.enemyShips[kill.PlayerId]; isOurs {
			continue
		}
		if ship, ok := s.enemyShips[kill.From]; ok {
			ship.Kills++
		}
	}
}

// tickEnemyShip advances one ship: escape a black hole if one is too
// close, otherwise chase and shoot at the nearest living player.
//
// A ship is flown the way a player flies one, not steered like a cursor:
// it picks a heading it *wants*, turns towards it at a fixed rate
// (SPEED.ROTATION) and always moves along the direction it's actually
// facing. Snapping the heading straight to the target and moving along
// that bearing instead made the ships pivot and change direction
// instantly, which read as obviously non-human.
func (s *npcSimulator) tickEnemyShip(enemyShip *enemyShipState, players map[string]PlayerData, now time.Time) {
	// Everything below is reasoned about in ship centers, not the top-left
	// anchors that travel on the wire, so distances/angles stay correct at
	// close range where half-a-ship offsets dominate.
	selfOffsetX, selfOffsetY := enemyShip.ship.centerOffset()
	selfX := enemyShip.X + selfOffsetX
	selfY := enemyShip.Y + selfOffsetY
	selfRadius := enemyShip.radius()

	// Black holes are just as lethal to an NPC ship as to a player, so
	// escaping one always outranks hunting: while in danger the ship turns
	// its back on the black hole, burns full throttle away from it and
	// holds its fire (its guns point where it's heading, like a player's).
	bhX, bhY, bhEdge, bhFound := s.nearestBlackHole(selfX, selfY, selfRadius)
	dangerRange := float64(enemyShipBlackHoleFear)
	if enemyShip.fleeing {
		dangerRange = enemyShipBlackHoleSafe
	}
	if bhFound && bhEdge <= dangerRange {
		enemyShip.fleeing = true
		escapeAngle := math.Atan2(selfY-bhY, selfX-bhX)
		s.steer(enemyShip, escapeAngle)
		enemyShip.Speed = math.Min(enemyShip.Speed+s.escapeAccel, s.escapeMaxSpeed)
		s.advance(enemyShip)
		s.applyBlackHoleGravity(enemyShip, selfOffsetX, selfOffsetY, selfRadius)
		return
	}
	enemyShip.fleeing = false

	target, found := s.nearestTarget(enemyShip, selfX, selfY, players)
	if !found {
		// Nobody to chase: coast to a stop, but gravity doesn't care.
		enemyShip.Speed = math.Max(enemyShip.Speed-s.decel, 0)
		s.advance(enemyShip)
		s.applyBlackHoleGravity(enemyShip, selfOffsetX, selfOffsetY, selfRadius)
		return
	}

	dx := target.x - selfX
	dy := target.y - selfY
	dist := math.Hypot(dx, dy)
	bearing := math.Atan2(dy, dx)

	// Blend in a push away from crowded neighbours so a group converging on
	// the same player fans out instead of piling onto one point.
	// The ship it is currently hunting is exempt from crowd avoidance: with
	// enemyShipsFightEachOther on, the separation radius is wider than the
	// range a ship wants to fight from, so avoiding its own target would
	// steer it away exactly when it is trying to line up - two rivals just
	// orbit each other and never fire. Overlap is still prevented, by
	// separateEnemyShips, which is positional rather than steering.
	crowdExempt := ""
	if target.isShip {
		crowdExempt = target.id
	}
	desired := s.avoidCrowding(enemyShip, selfX, selfY, selfRadius, bearing, crowdExempt)
	s.steer(enemyShip, desired)

	// Accelerate while closing in, brake once inside standoff range, so the
	// ship's speed actually varies over time instead of snapping between a
	// fixed value and zero.
	if dist > enemyShipStandoff {
		enemyShip.Speed += s.accel
		if enemyShip.Speed > s.maxSpeed {
			enemyShip.Speed = s.maxSpeed
		}
	} else {
		enemyShip.Speed -= s.decel
		if enemyShip.Speed < 0 {
			enemyShip.Speed = 0
		}
	}
	s.advance(enemyShip)

	// Only shoot when the nose is genuinely lined up: the guns fire along
	// the ship's facing (like a player's), so firing regardless of where
	// it's pointing would send bullets off at a visible angle to the ship.
	if dist <= enemyShipShootRange && now.Sub(enemyShip.lastShotAt) > s.settings.fireCooldown() {
		if math.Abs(angleDifference(float64(enemyShip.Rotate), bearing)) <= aimTolerance(target.radius, dist) {
			s.fireAt(enemyShip, now)
			enemyShip.lastShotAt = now
		}
	}

	s.applyBlackHoleGravity(enemyShip, selfOffsetX, selfOffsetY, selfRadius)
}

// steer turns the ship towards a desired heading by at most one frame's
// worth of rotation, taking the shortest way round - the same fixed turn
// rate a player gets from holding left/right (SPEED.ROTATION), rescaled to
// this service's tick rate.
func (s *npcSimulator) steer(enemyShip *enemyShipState, desired float64) {
	step := clientSpeedRotation * s.frameScale
	diff := angleDifference(float64(enemyShip.Rotate), desired)
	if math.Abs(diff) <= step {
		enemyShip.Rotate = float32(normalizeAngle(desired))
		return
	}
	enemyShip.Rotate = float32(normalizeAngle(float64(enemyShip.Rotate) + math.Copysign(step, diff)))
}

// advance moves the ship along the direction it is facing, like a player's
// movement() does - never along the bearing to its target, which would let
// it slide sideways while pointing somewhere else.
func (s *npcSimulator) advance(enemyShip *enemyShipState) {
	if enemyShip.Speed == 0 {
		return
	}
	heading := float64(enemyShip.Rotate)
	enemyShip.X += enemyShip.Speed * math.Cos(heading)
	enemyShip.Y += enemyShip.Speed * math.Sin(heading)
}

// avoidCrowding bends the heading a ship wants away from any neighbour
// that's too close, returning the blended heading. Steering (rather than
// only resolving overlaps after the fact) is what actually keeps a group
// apart: without it they converge on the same point and grind against each
// other permanently, with separation only ever undoing the last step.
func (s *npcSimulator) avoidCrowding(self *enemyShipState, selfX, selfY, selfRadius, desired float64, exemptId string) float64 {
	pushX, pushY := 0.0, 0.0
	for _, other := range s.enemyShips {
		if other == self || other.Id == exemptId {
			continue
		}
		otherOffsetX, otherOffsetY := other.ship.centerOffset()
		dx := selfX - (other.X + otherOffsetX)
		dy := selfY - (other.Y + otherOffsetY)
		distance := math.Hypot(dx, dy)
		wanted := (selfRadius + other.radius()) * enemyShipSeparationFactor
		if distance >= wanted {
			continue
		}
		if distance < 0.0001 {
			// Exactly co-located: pick an arbitrary but stable direction so
			// they can't stay welded together.
			angle := float64(self.Rotate)
			dx, dy, distance = math.Cos(angle), math.Sin(angle), 1
		}
		// Closer neighbours push harder.
		weight := (wanted - distance) / wanted
		pushX += (dx / distance) * weight
		pushY += (dy / distance) * weight
	}

	if pushX == 0 && pushY == 0 {
		return desired
	}
	// Combine as vectors so the result is a genuine compromise between
	// "go at the target" and "get out of the crowd".
	blendedX := math.Cos(desired) + pushX*enemyShipSeparationWeight
	blendedY := math.Sin(desired) + pushY*enemyShipSeparationWeight
	if blendedX == 0 && blendedY == 0 {
		return desired
	}
	return math.Atan2(blendedY, blendedX)
}

// separateEnemyShips pushes overlapping ships apart, mirroring ships-vue's
// checkShipBodyCollision (axis of least penetration) for the player. That
// check runs on each player's own client and can only move that player, so
// nothing was ever resolving NPC-against-NPC overlap: this service owns
// both ships, so it does it here, moving each one half the overlap so the
// result is symmetric.
func (s *npcSimulator) separateEnemyShips() {
	// Repeated until nothing moves: resolving pairs one at a time can push
	// a ship straight back into a pair that was already separated earlier
	// in the same pass, so with more than two ships in a pile a single pass
	// reliably leaves a few pixels of overlap behind. Capped so a
	// pathological arrangement can't stall the tick; whatever is left after
	// that is resolved by the next tick.
	for pass := 0; pass < enemyShipSeparationPasses; pass++ {
		if !s.separateEnemyShipsOnce() {
			return
		}
	}
}

// separateEnemyShipsOnce runs one resolution pass and reports whether it
// had to move anything.
func (s *npcSimulator) separateEnemyShipsOnce() bool {
	moved := false
	ships := make([]*enemyShipState, 0, len(s.enemyShips))
	for _, ship := range s.enemyShips {
		ships = append(ships, ship)
	}
	// Sorted so the resolution order (and therefore the outcome) doesn't
	// depend on Go's randomised map iteration.
	sort.Slice(ships, func(i, j int) bool { return ships[i].Id < ships[j].Id })

	for i := 0; i < len(ships); i++ {
		for j := i + 1; j < len(ships); j++ {
			a, b := ships[i], ships[j]
			aw, ah := a.ship.size()
			bw, bh := b.ship.size()

			overlapX := math.Min(a.X+aw, b.X+bw) - math.Max(a.X, b.X)
			overlapY := math.Min(a.Y+ah, b.Y+bh) - math.Max(a.Y, b.Y)
			if overlapX <= 0 || overlapY <= 0 {
				continue
			}

			moved = true
			if overlapX < overlapY {
				push := overlapX/2 + enemyShipSeparationEpsilon
				if a.X+aw/2 < b.X+bw/2 {
					a.X -= push
					b.X += push
				} else {
					a.X += push
					b.X -= push
				}
			} else {
				push := overlapY/2 + enemyShipSeparationEpsilon
				if a.Y+ah/2 < b.Y+bh/2 {
					a.Y -= push
					b.Y += push
				} else {
					a.Y += push
					b.Y -= push
				}
			}
		}
	}
	return moved
}

// aimTarget is whatever a ship has decided to attack, reduced to the only
// things the chase/aim code needs. Players and rival enemy ships are both
// expressed this way so neither is a special case further down.
type aimTarget struct {
	id     string
	x, y   float64 // center
	radius float64
	isShip bool // true when the target is another enemy ship
}

// nearestTarget picks what this ship should be hunting: the closest living
// player, or - when the admin has enabled enemyShipsFightEachOther - the
// closest of the players and the other enemy ships. A ship never targets
// itself, and a ship fleeing a black hole never gets here at all.
func (s *npcSimulator) nearestTarget(self *enemyShipState, x, y float64, players map[string]PlayerData) (aimTarget, bool) {
	var best aimTarget
	bestDist := math.MaxFloat64
	found := false

	for id, p := range players {
		if p.IsDead {
			continue
		}
		px, py := s.playerCenter(p)
		d := math.Hypot(px-x, py-y)
		if !found || d < bestDist {
			best = aimTarget{id: id, x: px, y: py, radius: s.playerRadius(p)}
			bestDist, found = d, true
		}
	}

	if s.settings.EnemyShipsFightEachOther {
		for id, other := range s.enemyShips {
			if other == self {
				continue
			}
			ox, oy := other.center()
			d := math.Hypot(ox-x, oy-y)
			if !found || d < bestDist {
				best = aimTarget{id: id, x: ox, y: oy, radius: other.radius(), isShip: true}
				bestDist, found = d, true
			}
		}
	}

	return best, found
}

// playerRadius resolves how big a target player is, for aim tolerance.
// This is the *drawn* size, so Scale matters here even though it doesn't
// move the center: a player's ship grows and shrinks with their score, and
// the victim's own client resolves our hits against getRealDimension().
// Ignoring it made a big ship flown at a small scale look several times
// wider than it is, so ships fired while still pointing well past it.
func (s *npcSimulator) playerRadius(target PlayerData) float64 {
	width, height, scale, ok := s.playerSize(target)
	if !ok {
		return 50
	}
	return math.Hypot(width*scale/2, height*scale/2)
}

// aimTolerance is how far off-target the nose may be and still be worth
// firing: the half-angle the target subtends at this distance, so a close
// target is easy to hit and a distant one demands a tighter line-up.
func aimTolerance(targetRadius, distance float64) float64 {
	if distance <= 0 {
		return enemyShipAimToleranceMax
	}
	tolerance := math.Atan2(targetRadius, distance)
	return math.Min(math.Max(tolerance, enemyShipAimToleranceMin), enemyShipAimToleranceMax)
}

// normalizeAngle wraps an angle into [0, 2*Pi), which is the range every
// angle on the wire uses (ships-vue's quadrant maths for bullet direction
// silently breaks for negative angles).
func normalizeAngle(angle float64) float64 {
	angle = math.Mod(angle, 2*math.Pi)
	if angle < 0 {
		angle += 2 * math.Pi
	}
	return angle
}

// angleDifference returns the signed shortest rotation from `from` to `to`,
// in (-Pi, Pi] - so turning is always the short way round.
func angleDifference(from, to float64) float64 {
	diff := math.Mod(to-from, 2*math.Pi)
	if diff > math.Pi {
		diff -= 2 * math.Pi
	}
	if diff < -math.Pi {
		diff += 2 * math.Pi
	}
	return diff
}

// applyBlackHoleGravity drags the enemy ship towards every nearby black
// hole, and destroys it if it reaches one, using the exact same maths
// ships-vue runs for the local player (applyNpcImpacts). It's applied after
// the ship's own movement, mirroring the client, where the pull is layered
// on top of whatever the player's controls did that frame - which is what
// makes escaping a matter of out-running the pull rather than being
// immune to it.
//
// centerOffset*/radius are passed in because the caller already resolved
// them; positions are stored as the ship's top-left anchor.
func (s *npcSimulator) applyBlackHoleGravity(enemyShip *enemyShipState, offsetX, offsetY, radius float64) {
	x := enemyShip.X + offsetX
	y := enemyShip.Y + offsetY

	for _, npc := range s.npcs {
		if npc.Type != NpcTypes.BlackHole {
			continue
		}
		bhX, bhY, bhRadius := blackHoleBody(npc)
		centerDistance := math.Hypot(bhX-x, bhY-y)
		if centerDistance <= 0.0001 {
			s.killEnemyShipByBlackHole(enemyShip)
			return
		}
		edgeDistance := centerDistance - (radius + bhRadius)
		if edgeDistance > blackHolePullRange {
			continue
		}

		// The closer it gets, the harder it's pulled - never past the
		// black hole's center in a single step.
		safeDistance := math.Max(edgeDistance, 1)
		jumpStep := math.Min((blackHolePullFactor/safeDistance)*blackHolePullBoost*s.frameScale, centerDistance)
		angle := math.Atan2(bhY-y, bhX-x)
		x += math.Cos(angle) * jumpStep
		y += math.Sin(angle) * jumpStep

		if jumpStep >= centerDistance-0.0001 {
			enemyShip.X = x - offsetX
			enemyShip.Y = y - offsetY
			s.killEnemyShipByBlackHole(enemyShip)
			return
		}
	}

	enemyShip.X = x - offsetX
	enemyShip.Y = y - offsetY
}

// killEnemyShipByBlackHole announces the death through the same playerDied
// event a bullet kill uses, but with no killer: ships-go's handler looks up
// Players[""], finds nobody and credits no one, and ships-vue falls back to
// the black hole explosion animation (killedByEnemyShot needs a `from`).
func (s *npcSimulator) killEnemyShipByBlackHole(enemyShip *enemyShipState) {
	died := playerDiedMsg{
		PlayerId: enemyShip.Id,
		X:        float32(enemyShip.X),
		Y:        float32(enemyShip.Y),
	}
	if err := s.sender.sendPlayerDied(died); err != nil {
		log.Println("ships-npc: failed to announce enemy ship black hole death:", err)
	}
	enemyShip.Deaths++
	s.retireEnemyShip(enemyShip)
	delete(s.enemyShips, enemyShip.Id)
	s.enemyRespawnAt = time.Now().Add(enemyShipRespawnDelay)
}

// nearestBlackHole returns the center and edge distance of the black hole
// currently closest to a body of the given radius at (x, y) - "closest"
// being measured edge-to-edge, since a big black hole is dangerous from
// much further away than a small one.
func (s *npcSimulator) nearestBlackHole(x, y, radius float64) (bhX, bhY, edgeDistance float64, found bool) {
	for _, npc := range s.npcs {
		if npc.Type != NpcTypes.BlackHole {
			continue
		}
		cx, cy, bhRadius := blackHoleBody(npc)
		edge := math.Hypot(cx-x, cy-y) - (radius + bhRadius)
		if !found || edge < edgeDistance {
			bhX, bhY, edgeDistance, found = cx, cy, edge, true
		}
	}
	return
}

// blackHoleBody resolves a black hole's collision body the way ships-vue
// does: its Animation is a MaxSize square scaled by Scale, anchored at its
// top-left, and its radius is the half-diagonal of that square
// (getRealDimension + getRadiusFromRect).
func blackHoleBody(bh *npcState) (centerX, centerY, radius float64) {
	size := float64(bh.MaxSize) * bh.Scale
	return bh.X + size/2, bh.Y + size/2, (size / 2) * math.Sqrt2
}

// fireAt sends a newBullet event along the ship's nose, exactly like a
// player's client would, so every client renders/animates it identically
// with no ships-vue changes needed.
//
// The shot follows the ship's *facing*, not the bearing to the target: a
// player's guns are fixed forward, and firing along the bearing instead
// made bullets leave the ship at a visible angle whenever it hadn't
// finished turning. The caller is responsible for only firing when the
// nose is lined up (see aimTolerance).
func (s *npcSimulator) fireAt(enemyShip *enemyShipState, now time.Time) {
	offsetX, offsetY := enemyShip.ship.centerOffset()
	originX := enemyShip.X + offsetX
	originY := enemyShip.Y + offsetY

	// ships-vue's Bullet class (and every other angle sent over the wire,
	// e.g. a player's own rotate) expects angles normalized to [0, 2*Pi),
	// not atan2's (-Pi, Pi] range - its quadrant-sign-correction trick for
	// moveX/moveY silently breaks for negative angles otherwise.
	angle := normalizeAngle(float64(enemyShip.Rotate))
	moveX := math.Cos(angle)
	moveY := math.Sin(angle)

	id := enemyShip.Id + "-" + uuid.NewString()
	bullet := newBulletMsg{
		SocketId:      enemyShip.Id,
		Id:            id,
		Angle:         float32(angle),
		Rotation:      float32(angle),
		BulletCharge:  npcBulletCharge,
		ShootingSpeed: npcBulletShootingSpeed,
		X:             float32(originX),
		Y:             float32(originY),
		MoveX:         float32(moveX),
		MoveY:         float32(moveY),
		ExpX:          float32(moveX*npcBulletRange + originX),
		ExpY:          float32(moveY*npcBulletRange + originY),
	}
	if err := s.sender.sendBullet(bullet); err != nil {
		log.Println("ships-npc: failed to send enemy ship bullet:", err)
		return
	}
	s.activeBullets[id] = &npcBullet{
		ownerId: enemyShip.Id,
		x:       originX,
		y:       originY,
		stepX:   moveX * npcBulletSpeed * s.frameScale,
		stepY:   moveY * npcBulletSpeed * s.frameScale,
		firedAt: now,
	}
}

// advanceBullets flies this service's own bullets one tick, resolves the
// NPC-vs-NPC hits nobody else can (see activeBullets), and retires bullets
// that have had enough time to travel their full range.
//
// Bullets are always flown, even with enemyShipsFightEachOther off: a
// dormant trajectory costs nothing and it means turning the toggle on takes
// effect on the very next tick instead of only for bullets fired after it.
func (s *npcSimulator) advanceBullets(now time.Time) {
	friendlyFire := s.settings.EnemyShipsFightEachOther

	for id, bullet := range s.activeBullets {
		if now.Sub(bullet.firedAt) >= npcBulletLifetime {
			s.retireBullet(id, "expired")
			continue
		}

		// The swept segment, not just the new point: at the default tick
		// rate a bullet covers more than a ship's width per tick, so a
		// point test would pass straight through its target.
		fromX, fromY := bullet.x, bullet.y
		bullet.x += bullet.stepX
		bullet.y += bullet.stepY

		if !friendlyFire {
			continue
		}
		victim, hit := s.enemyShipHitBy(bullet, fromX, fromY)
		if !hit {
			continue
		}
		// Removed rather than just deleted: the bullet everyone can see is
		// fed by the newBullet broadcast and only an explicit removeBullet
		// clears it, so otherwise it would visibly fly on through the ship
		// it had already destroyed.
		s.retireBullet(id, "spent")
		s.damageEnemyShip(victim, id, bullet.ownerId, npcBulletCharge)
	}
}

// retireBullet stops tracking a bullet and tells everyone to stop drawing
// it. Callers hold s.mu.
func (s *npcSimulator) retireBullet(id, reason string) {
	if err := s.sender.sendRemoveBullet(id); err != nil {
		log.Println("ships-npc: failed to remove "+reason+" bullet:", err)
	}
	delete(s.activeBullets, id)
}

// enemyShipHitBy returns the first enemy ship the bullet's path this tick
// passes through, ignoring the ship that fired it (its own bullet starts
// inside its own hull and would otherwise kill it instantly). When the path
// crosses more than one, the nearest along the path wins, so a bullet can't
// hit a ship behind the one it should have struck first.
func (s *npcSimulator) enemyShipHitBy(bullet *npcBullet, fromX, fromY float64) (*enemyShipState, bool) {
	var hit *enemyShipState
	bestT := math.MaxFloat64

	for _, ship := range s.enemyShips {
		if ship.Id == bullet.ownerId {
			continue
		}
		centerX, centerY := ship.center()
		t, ok := segmentHitsCircle(fromX, fromY, bullet.x, bullet.y, centerX, centerY, ship.radius())
		if ok && t < bestT {
			hit, bestT = ship, t
		}
	}
	return hit, hit != nil
}

// segmentHitsCircle reports whether the segment (x1,y1)-(x2,y2) comes
// within r of (cx,cy), and how far along the segment (0..1) the closest
// approach is.
func segmentHitsCircle(x1, y1, x2, y2, cx, cy, r float64) (float64, bool) {
	dx, dy := x2-x1, y2-y1
	lengthSq := dx*dx + dy*dy

	t := 0.0
	if lengthSq > 0 {
		t = ((cx-x1)*dx + (cy-y1)*dy) / lengthSq
		t = math.Max(0, math.Min(1, t))
	}

	closestX, closestY := x1+t*dx, y1+t*dy
	return t, math.Hypot(closestX-cx, closestY-cy) <= r
}

// handleNpcHit applies damage reported by a player's client (its bullet hit
// our enemy ship). If this kills it, credits the killer and starts a
// respawn timer via ships-go's existing playerDied handling.
func (s *npcSimulator) handleNpcHit(hit npcHitMsg) {
	s.mu.Lock()
	defer s.mu.Unlock()

	enemyShip, ok := s.enemyShips[hit.NpcId]
	if !ok {
		return
	}

	// The shooter's client only removes its own logical/collision-tracking
	// copy of the bullet locally. The bullet that's actually rendered on
	// every screen (including the shooter's) is a separate copy fed by the
	// newBullet broadcast, and it's only ever cleared by an explicit
	// removeBullet event - mirroring how a hit player's client requests
	// this for player-vs-player hits. Without this, the bullet visually
	// keeps flying through the NPC even though it already dealt damage.
	if err := s.sender.sendRemoveBullet(hit.BulletId); err != nil {
		log.Println("ships-npc: failed to remove spent bullet:", err)
	}

	s.damageEnemyShip(enemyShip, hit.BulletId, hit.From, hit.BulletCharge)
}

// damageEnemyShip applies damage to one enemy ship and, if that kills it,
// announces the death through ships-go's normal playerDied handling and
// retires the identity for a later respawn. Shared by the two sources of
// damage: a player's client reporting its own bullet hit (handleNpcHit),
// and this service resolving its own NPC-vs-NPC bullets (advanceBullets).
// Callers hold s.mu.
func (s *npcSimulator) damageEnemyShip(enemyShip *enemyShipState, bulletId, fromId string, charge float32) {
	enemyShip.Life -= charge
	if enemyShip.Life > 0 {
		return
	}

	died := playerDiedMsg{
		BulletId: bulletId,
		From:     fromId,
		PlayerId: enemyShip.Id,
		X:        float32(enemyShip.X),
		Y:        float32(enemyShip.Y),
	}
	if err := s.sender.sendPlayerDied(died); err != nil {
		log.Println("ships-npc: failed to announce enemy ship death:", err)
	}
	enemyShip.Deaths++

	// An NPC killer is credited here rather than in recordKills. ships-go
	// does broadcast this death back to us, but recordKills ignores any
	// kill whose victim is one of our own ships (so a ship can never be
	// credited for its own death), which would leave an NPC-vs-NPC kill
	// counted nowhere at all.
	if killer, ok := s.enemyShips[fromId]; ok && killer != enemyShip {
		killer.Kills++
	}

	s.retireEnemyShip(enemyShip)
	delete(s.enemyShips, enemyShip.Id)
	s.enemyRespawnAt = time.Now().Add(enemyShipRespawnDelay)
}

func spawnEnemyShip(players map[string]PlayerData, ships []publicShip, now time.Time, maxLife float32) *enemyShipState {
	minX, minY, maxX, maxY := spawnBounds(players, enemyShipSpawnMargin)
	rangeX := maxX - minX
	rangeY := maxY - minY
	ship := ships[rand.Intn(len(ships))]

	return &enemyShipState{
		spawnedAt: now,
		ship:      ship,
		NpcData: NpcData{
			Type:   NpcTypes.Ship,
			Id:     uuid.NewString(),
			ShipId: ship.Id,
			Name:   "Enemy " + ship.Name,
			X:      rand.Float64()*float64(rangeX) + float64(minX),
			Y:      rand.Float64()*float64(rangeY) + float64(minY),
			// A random heading, so a fleet doesn't appear all facing the
			// same way and each ship has a different distance to turn
			// before it can open fire.
			Rotate:  float32(rand.Float64() * 2 * math.Pi),
			Scale:   enemyShipScale,
			Life:    maxLife,
			MaxLife: maxLife,
		},
	}
}

// spawnBounds returns a bounding box around the current players, expanded
// by margin, used to pick a random spawn point that's near the action but
// not right on top of anyone.
func spawnBounds(players map[string]PlayerData, margin float32) (minX, minY, maxX, maxY float32) {
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
	minX -= margin
	maxX += margin
	minY -= margin
	maxY += margin
	return
}

func spawnBlackHole(players map[string]PlayerData, now time.Time) *npcState {
	minX, minY, maxX, maxY := spawnBounds(players, blackHoleSpawnMargin)
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
