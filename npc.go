package main

import (
	"fmt"
	"log"
	"math"
	"math/rand"
	"sort"
	"sync"
	"time"

	"github.com/google/uuid"
)

const (
	blackHoleSpawnMargin = 2000
	blackHoleScaleStep   = 0.01
	blackHoleSpeed       = 7.5

	// How long a black hole takes to grow to full size, and to collapse
	// again at the end of its life. Capped at a third of its configured
	// duration, so a short-lived one is still fully open for a third of
	// it rather than being a dot that never opens.
	blackHoleGrowthTime = 3 * time.Second

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
	// Ramming hurts. Mirrored from ships-vue's SHIP_RAM_DAMAGE /
	// SHIP_RAM_COOLDOWN_MS, because a player and an NPC must take the same
	// damage on the same contact - the collision is resolved in two
	// different places (a player's browser damages the player, this service
	// damages its own ships) and the two would otherwise disagree about
	// what just happened. Keep them in sync.
	enemyShipRamDamage   = 2
	enemyShipRamCooldown = 800 * time.Millisecond
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
	// How long a destroyed ship stays out of the game. Each ship gets its
	// own clock, started when it dies, plus a random extra up to
	// enemyShipRespawnJitter: a fleet wiped out together must not come back
	// together, and even without that the identical delay made a hundred
	// ships reappear on the same tick. Roughly matches what a player waits
	// (2s dying, 10s dead) so no fleet has an advantage in tempo.
	enemyShipRespawnDelay  = 8 * time.Second
	enemyShipRespawnJitter = 4 * time.Second
	// Minimum half-size of the box ships spawn into, measured out from the
	// players' bounding box. It is a floor, not the whole story: see
	// spawnArea, which grows the box with the size of the fleet.
	enemyShipSpawnMargin = 1500
	// Room each ship wants to itself at spawn, center to center. The spawn
	// box is sized to give the whole fleet this much each, and candidate
	// points closer than this to an existing ship or a player are rejected.
	// Without it a 200-ship fleet materialises in a heap: uniform random
	// points in a fixed box clump badly, and a fixed box that comfortably
	// holds one ship is standing room only for two hundred.
	enemyShipSpawnSpacing = 600.0
	// How many candidate points are tried before settling for the roomiest
	// one seen. Bounded so a genuinely full box cannot stall the tick; the
	// best-of-N fallback keeps the result sane rather than random.
	enemyShipSpawnTries    = 12
	npcBulletShootingSpeed = 0 // matches ships-vue's default (no charge bonus)
	npcBulletSpeed         = 25*1.5 + npcBulletShootingSpeed
	npcBulletRange         = 5000
	npcBulletCharge        = 3 // damage per hit; player life defaults to 10
	npcBulletLifetime      = 3 * time.Second

	// Evasion. A ship looks this far ahead along each incoming bullet's
	// path; anything that will not reach it within that window is not worth
	// breaking off an attack for, and anything already past is not a threat
	// at all.
	enemyShipDodgeLookahead = 1200 * time.Millisecond
	// How much wider than the hull the danger corridor is. A bullet that
	// will pass a whisker away is still worth avoiding: the ship is moving
	// too, and it is aiming, so its own manoeuvring can put it back in the
	// path.
	enemyShipDodgeMargin = 2.2
	// How long a dodge is committed to once started. Without it the ship
	// re-decides every tick, flips between the two ways out of the way and
	// travels nowhere - and a ship that turns 6 degrees a tick needs the
	// time to actually get its nose round.
	enemyShipDodgeCommit = 700 * time.Millisecond
	// The speed a ship keeps while fighting, as a fraction of its cruising
	// speed. It never brakes below this: a ship at a standstill is a free
	// target, and a player who stops shooting to stop moving is a dead one.
	// It is slow rather than fast on purpose - a ship turns at a fixed
	// 0.02 radians per frame whatever its speed, so its turning circle is
	// proportional to it. At full speed that circle is wider than the range
	// it fights from, which is why a break-off used to fling it out of
	// range entirely; at a quarter speed it can turn inside its own
	// standoff distance and stay in the fight.
	enemyShipFightSpeedFactor = 0.35
	// Once in range a ship alternates between lining up (nose on the
	// target, shooting) and breaking away (nose off it, repositioning for
	// the next pass). Neither half is optional. The guns fire along the
	// heading, so a ship can only shoot while pointing at its target, and
	// the aim is narrow enough that it has to hold that point for a beat
	// to land anything - which means holding a course, which is exactly
	// what makes it easy to shoot back at. The cycle is the compromise:
	// it gives up most of the rate of fire a parked turret would have in
	// exchange for never being one.
	enemyShipAimPhase    = 900 * time.Millisecond
	enemyShipMovePhase   = 800 * time.Millisecond
	enemyShipPhaseJitter = 600 * time.Millisecond
	// While breaking away the ship weaves rather than flying a straight
	// line out: this far either side of its escape heading, on this
	// period, with the rate jittered per ship so a fleet does not sweep
	// as one body. A straight line is the easiest thing there is to lead
	// a shot on, and the break-off is when the ship is not shooting back.
	enemyShipWeaveAngle  = 40 * math.Pi / 180
	enemyShipWeavePeriod = 1800 * time.Millisecond
	enemyShipWeaveJitter = 0.25
	// Hard limits on that dance: closer than this fraction of the standoff
	// distance it always moves away (contact damage is mutual, and ramming
	// is not the plan), and past this multiple it always closes back in.
	enemyShipTooCloseRange = 0.8
	enemyShipBreakoffRange = 1.7
	// How far off the bearing to the target a breaking-away ship aims. Not
	// a full 180: it keeps the target in view and comes round in an arc
	// rather than fleeing in a straight line.
	enemyShipBreakoffAngle = 75 * math.Pi / 180
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
	// retiredShips holds the identities of ships that have been destroyed
	// (or despawned when the fleet shrank), newest last. Respawning reuses
	// one instead of inventing a new uuid/name, so an NPC keeps its name
	// and its kill/death record across deaths - which is what makes it
	// worth listing in ships-vue's scoreboard at all, and reads like a
	// recurring rival rather than an endless parade of strangers.
	// Kept per controller, because an identity belongs to its fleet: a
	// retired [AI] ship must not come back flown by the rule controller
	// with an [AI] name, and vice versa.
	retiredShips map[controllerKind][]*enemyShipState
	// spawnCount numbers the ships in each fleet, so a hundred of them are
	// not all called "[NPC] Falcon".
	spawnCount map[controllerKind]int
	// ramHitAt is the last time each pair of touching ships damaged each
	// other, keyed "idA|idB" with the lower id first. It is the NPC-side
	// equivalent of ships-vue's shipRamHitAt, and is pruned every tick.
	ramHitAt map[string]time.Time
	// activeBullets are the bullets this service has fired and not yet
	// retired. Their trajectory is kept (not just the time they were
	// fired) because NPC-vs-NPC hits have to be resolved here: no client
	// tracks them. Player-vs-NPC still arrives as an npcHit from the
	// shooting player's own client, and NPC-vs-player is still that
	// player's client's job, exactly as before.
	activeBullets map[string]*npcBullet

	// incomingBullets are the shots fired by *players*, learned from the
	// gameBroadcast relay. This service does not resolve them (the victim's
	// own client does that, and for our ships the shooter's client sends an
	// npcHit), so they are kept for one reason only: a ship that cannot see
	// incoming fire cannot avoid it, and one that never avoids it is a
	// target rather than an opponent. Our own fleet's bullets are already
	// in activeBullets and are read from there.
	incomingBullets map[string]*npcBullet

	// policy is the learned controller flown by the [AI] fleet, loaded
	// once at startup from the weights embedded in the binary. Nil when
	// those weights are unusable, in which case AI ships fall back to the
	// rule controller rather than flying blind.
	policy *aiPolicy

	// observer, when set, is handed every decision as it is taken. Only
	// the offline trainer sets it: it is how training data is captured
	// through the exact same code path that runs in production, so the
	// features the policy learns from cannot drift from the ones it is
	// later asked to act on.
	observer func(view shipView, command shipCommand)

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
	// weaveStep is how far the attack weave advances each tick, derived
	// from the tick rate so the manoeuvre lasts the same wall-clock time
	// whatever rate this service runs at.
	weaveStep float64

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
	ownerId string
	// ownerController decides who this bullet may damage, and is stored
	// rather than looked up because the ship that fired it may already be
	// dead by the time the bullet lands.
	ownerController controllerKind
	x, y            float64
	stepX, stepY    float64 // px per tick, already scaled by frameScale
	firedAt         time.Time
}

// enemyShipState is the enemy ship's own npcState, kept separate from
// npcState (black holes) since it carries extra fields (life, target).
type enemyShipState struct {
	NpcData
	controller controllerKind // which brain flies it; also its faction
	ship       publicShip     // needed to fire bullets from the ship's actual center
	spawnedAt  time.Time
	fleeing    bool      // escaping a black hole; see enemyShipBlackHoleSafe
	lastShotAt time.Time // per ship, so several of them don't fire in lockstep
	// evadeUntil is how long the ship stays committed to a dodge. A
	// manoeuvre that is reconsidered every tick is no manoeuvre at all:
	// the ship would flip sides as the geometry shifts and stay exactly
	// where it was. evadeAngle is the heading it is dodging towards.
	evadeUntil time.Time
	evadeAngle float64
	// breakingAway is set when a ship has closed inside its standoff
	// distance and is flying past its target to come round again, instead
	// of parking in front of it. Hysteresis, like fleeing. breakoffSide is
	// which way it turned, chosen once on the way in: re-deciding it every
	// tick let it flip sides while its nose was on the target, which
	// averaged out to flying straight through it.
	breakingAway bool
	breakoffSide float64
	// weavePhase is where the ship is in its weave, in radians. Each ship
	// gets its own starting point so a fleet attacking the same player
	// does not swing from side to side as one body, and its own rate, so
	// they do not fall into step later either.
	weavePhase float64
	weaveRate  float64
	// phaseSkew is where in its jitter range this ship's aim and break-off
	// phases sit, from -1 to 1. Fixed per ship rather than rolled every
	// time a phase starts, so a ship's flying is reproducible.
	phaseSkew float64
	// phaseUntil is when the current half of the aim/break-off cycle ends.
	phaseUntil time.Time
	// respawnAt is when this identity may come back, set when it dies.
	// Per ship rather than per fleet: a shared clock meant one death
	// delayed everybody, and a wipe brought the whole fleet back on a
	// single tick.
	respawnAt time.Time
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
		retiredShips:    make(map[controllerKind][]*enemyShipState),
		spawnCount:      make(map[controllerKind]int),
		ramHitAt:        make(map[string]time.Time),
		activeBullets:   make(map[string]*npcBullet),
		incomingBullets: make(map[string]*npcBullet),
		policy:          loadAiPolicy(),
		frameScale:      frameScale,
		weaveStep:       2 * math.Pi * float64(tickInterval) / float64(enemyShipWeavePeriod),
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
	// The standard size is a live setting, and a ship's scale is what
	// every distance in here is measured against - how close is a ram, how
	// big a target is, how far apart to hold two ships. Ships already
	// flying have to be re-scaled or the fleet on the map would keep its
	// old geometry while reinforcements arrive with the new one, exactly
	// as ships-vue re-measures the ships already on screen.
	for _, enemyShip := range s.enemyShips {
		enemyShip.Scale = s.shipScale(enemyShip.ship)
	}
}

// shipScale is how big a ship is actually drawn, mirroring ships-vue's
// Player.calculateScale: every hull is normalised to the standard size,
// whatever its artwork measures. Getting this wrong is not cosmetic - the
// scale is the ship's collision box, so a 400px hull left at scale 1 would
// be rammed, separated and shot at from four times the distance a player
// can see it.
func (s *npcSimulator) shipScale(ship publicShip) float64 {
	width, height := ship.size()
	base := math.Max(width, height)
	if base <= 0 {
		return 1
	}
	size := float64(s.settings.ShipSize)
	if size <= 0 {
		size = defaultShipSize
	}
	return size / base
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

	// One player is enough: black holes are a hazard for the NPC fleet too,
	// and enemy ships already spawn from a single player, so requiring two
	// meant nobody testing alone ever saw one.
	if len(players) > 0 && len(s.npcs) < s.settings.MaxBlackHoles && now.Sub(s.lastSpawn) > s.settings.blackHoleSpawnPeriod() {
		bh := spawnBlackHole(players, now, s.settings.blackHoleDuration())
		s.npcs[bh.Id] = bh
		s.lastSpawn = now
	}

	toRemove := make([]string, 0)
	for id, npc := range s.npcs {
		elapsed := now.Sub(npc.spawnedAt)
		duration := s.settings.blackHoleDuration()
		if elapsed >= duration {
			toRemove = append(toRemove, id)
			continue
		}
		npc.Scale = blackHoleScaleAt(elapsed, duration)

		radians := npc.Direction * (math.Pi / 180)
		npc.X += npc.Speed * math.Cos(radians)
		npc.Y += npc.Speed * math.Sin(radians)
	}

	for _, id := range toRemove {
		delete(s.npcs, id)
	}

	s.tickEnemyShips(players, now)
	s.advanceBullets(now)
	s.advanceIncomingBullets(now)
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
	// Damage is settled before the ships are pushed apart, since separation
	// is what ends the contact.
	s.ramEnemyShips(now)
	s.separateEnemyShips()
}

// manageFleetSize spawns ships up to the configured count and despawns any
// excess after the count is lowered. A despawn is deliberately silent (no
// playerDied): nobody killed them, so an explosion and a kill-feed entry
// would be a lie. ships-vue drops any NPC missing from the batch, so
// leaving them out of the next snapshot is all that's needed.
func (s *npcSimulator) manageFleetSize(players map[string]PlayerData, now time.Time) {
	for _, controller := range allControllers {
		s.manageFleet(controller, players, now)
	}
}

// manageFleet does that for one fleet. The two are sized independently, so
// turning the AI fleet off (or down) never disturbs the rule-flown one.
func (s *npcSimulator) manageFleet(controller controllerKind, players map[string]PlayerData, now time.Time) {
	desired := s.settings.fleetSize(controller)
	alive := s.fleetCount(controller)

	for id, ship := range s.enemyShips {
		if alive <= desired {
			break
		}
		if ship.controller != controller {
			continue
		}
		s.retireEnemyShip(ship)
		delete(s.enemyShips, id)
		alive--
	}

	if alive >= desired || len(players) == 0 || len(s.ships) == 0 {
		return
	}

	// Each dead ship comes back on its own clock, so a fleet trickles back
	// in the order it died instead of the whole thing reappearing at once
	// the moment a single shared timer expires.
	for alive < desired {
		ship := s.reviveReadyShip(controller, players, now)
		if ship == nil {
			break
		}
		s.enemyShips[ship.Id] = ship
		alive++
	}

	// Whatever is still missing is a fleet that has grown, not one waiting
	// on the dead: there are more places than identities to fill them. Those
	// ships are brand new and appear immediately, which is what makes the
	// initial fleet - and any increase from the admin panel - instant.
	for extra := desired - alive - len(s.retiredShips[controller]); extra > 0; extra-- {
		ship := s.spawnEnemyShip(controller, players, now)
		s.enemyShips[ship.Id] = ship
	}
}

// reviveReadyShip brings back the longest-dead identity whose respawn delay
// has elapsed, or nil when every one of them is still waiting.
func (s *npcSimulator) reviveReadyShip(controller controllerKind, players map[string]PlayerData, now time.Time) *enemyShipState {
	retired := s.retiredShips[controller]
	for i, ship := range retired {
		if now.Before(ship.respawnAt) {
			continue
		}
		s.retiredShips[controller] = append(retired[:i:i], retired[i+1:]...)
		return s.respawn(ship, players, now)
	}
	return nil
}

// fleetCount is how many ships of one controller are currently in play.
func (s *npcSimulator) fleetCount(controller controllerKind) int {
	count := 0
	for _, ship := range s.enemyShips {
		if ship.controller == controller {
			count++
		}
	}
	return count
}

// retireEnemyShip parks a ship's identity so the next respawn can bring it
// back with the same id, name, hull and score.
func (s *npcSimulator) retireEnemyShip(enemyShip *enemyShipState) {
	// Despawning is not dying: a ship taken off the map because the fleet
	// shrank owes no respawn delay, so raising the size again brings it
	// straight back.
	enemyShip.respawnAt = time.Time{}
	s.retiredShips[enemyShip.controller] = append(s.retiredShips[enemyShip.controller], enemyShip)
}

// retireDeadEnemyShip is the same, for a ship that was destroyed: it starts
// that ship's own respawn clock. The jitter matters as much as the delay -
// a fleet wiped out together would otherwise still come back together.
func (s *npcSimulator) retireDeadEnemyShip(enemyShip *enemyShipState) {
	s.retireEnemyShip(enemyShip)
	enemyShip.respawnAt = time.Now().Add(enemyShipRespawnDelay + time.Duration(rand.Int63n(int64(enemyShipRespawnJitter)+1)))
}

// respawn puts a retired identity back in the game, resetting only what a
// new life resets (position, health, heading, throttle) and keeping its
// name, hull and score.
func (s *npcSimulator) respawn(revived *enemyShipState, players map[string]PlayerData, now time.Time) *enemyShipState {
	// A fresh spawn point, but deliberately not a whole spawnEnemyShip:
	// that would consume a fleet number and leave gaps in the names.
	x, y := s.spawnPoint(players)
	revived.spawnedAt = now
	revived.fleeing = false
	revived.lastShotAt = time.Time{}
	revived.X = x
	revived.Y = y
	revived.Rotate = float32(rand.Float64() * 2 * math.Pi)
	revived.Speed = 0
	revived.startWeave()
	revived.Scale = s.shipScale(revived.ship)
	revived.Life = s.settings.ShipLife
	revived.MaxLife = s.settings.ShipLife
	return revived
}

// startWeave gives the ship its own place in, and speed through, the
// attack weave. Ships that share both fly as one wide target.
func (e *enemyShipState) startWeave() {
	e.weavePhase = rand.Float64() * 2 * math.Pi
	e.weaveRate = 1 + (rand.Float64()*2-1)*enemyShipWeaveJitter
	e.phaseSkew = rand.Float64()*2 - 1
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
		// Nothing it is allowed to attack: coast to a stop, but gravity
		// doesn't care.
		enemyShip.Speed = math.Max(enemyShip.Speed-s.decel, 0)
		s.advance(enemyShip)
		s.applyBlackHoleGravity(enemyShip, selfOffsetX, selfOffsetY, selfRadius)
		return
	}

	view := s.observe(enemyShip, selfX, selfY, selfRadius, target)
	view.bhFound, view.bhX, view.bhY, view.bhEdge = bhFound, bhX, bhY, bhEdge
	view.fireReady = now.Sub(enemyShip.lastShotAt) > s.settings.fireCooldown()
	view.threatAngle, view.threatUrgency, view.threatFound = s.nearestThreat(enemyShip, selfX, selfY, selfRadius)
	view.now = now
	// The attack cycle is advanced here, not inside a brain: it is part of
	// what a ship is told about its situation, like the black hole and the
	// bullet, and an AI ship that could not see it would have to average
	// the two halves of the manoeuvre together and fly neither.
	s.advanceAttackPhase(view)

	var command shipCommand
	if enemyShip.controller == controllerAi && s.policy != nil {
		command = s.policy.decide(s.features(view, view.fireReady))
	} else {
		command = s.ruleDecision(view)
	}
	if s.observer != nil {
		s.observer(view, command)
	}
	s.applyCommand(enemyShip, command, now)

	s.applyBlackHoleGravity(enemyShip, selfOffsetX, selfOffsetY, selfRadius)
}

// shipCommand is what a brain decides for one ship in one tick, expressed
// in the controls a player has rather than in absolute positions: turn is
// a fraction of one tick's rotation (negative left, positive right),
// thrust a fraction of one tick's acceleration (negative brakes), fire a
// request to shoot if the gun is off cooldown.
//
// Both brains emit this and applyCommand executes it, so the rule
// controller and the learned policy fly on exactly the same physics - the
// only honest way to compare them, and the reason the AI cannot cheat by
// turning faster or shooting sooner than the rules allow.
type shipCommand struct {
	turn   float64
	thrust float64
	fire   bool
}

// shipView is everything a brain is told about the world. The rule
// controller reads the raw fields; the learned policy reads the normalized
// vector built from them by features(). Keeping one struct means both
// brains provably see the same situation.
type shipView struct {
	self    *enemyShipState
	target  aimTarget
	x, y    float64 // own center
	radius  float64
	dist    float64
	bearing float64 // absolute angle to the target
	// desired is the bearing after crowd avoidance has been blended in.
	// It is computed for both brains: keeping ships from piling onto one
	// point is a hard rule here, not something a policy has to learn.
	desired float64
	// The nearest black hole, if any is close enough to matter. Escaping
	// one is handled before either brain runs (it is a safety rule, not a
	// decision), but the policy is still told about it so it can avoid
	// steering back towards one it has just escaped.
	bhFound   bool
	bhX, bhY  float64
	bhEdge    float64
	fireReady bool
	// The most pressing bullet on its way to this ship, if any.
	// threatAngle is the direction the bullet is travelling, and urgency
	// is 1 when it is about to arrive and 0 when it is at the edge of the
	// lookahead window - so "no bullet" and "a bullet a long way off" are
	// the same number rather than opposite ends of the range.
	threatFound   bool
	threatAngle   float64
	threatUrgency float64
	// now is this tick's timestamp, so a brain can time a manoeuvre
	// without reaching for the wall clock (and so tests can drive it).
	now time.Time
}

// observe gathers the world state for one ship.
func (s *npcSimulator) observe(enemyShip *enemyShipState, selfX, selfY, selfRadius float64, target aimTarget) shipView {
	dx := target.x - selfX
	dy := target.y - selfY
	bearing := math.Atan2(dy, dx)

	// Blend in a push away from crowded neighbours so a group converging on
	// the same player fans out instead of piling onto one point.
	// The ship it is currently hunting is exempt from crowd avoidance: when
	// ships hunt each other the separation radius is wider than the range a
	// ship wants to fight from, so avoiding its own target would steer it
	// away exactly when it is trying to line up - two rivals just orbit
	// each other and never fire. Overlap is still prevented, by
	// separateEnemyShips, which is positional rather than steering.
	crowdExempt := ""
	if target.isShip {
		crowdExempt = target.id
	}

	return shipView{
		self:    enemyShip,
		target:  target,
		x:       selfX,
		y:       selfY,
		radius:  selfRadius,
		dist:    math.Hypot(dx, dy),
		bearing: bearing,
		desired: s.avoidCrowding(enemyShip, selfX, selfY, selfRadius, bearing, crowdExempt),
	}
}

// ruleDecision is the hand-written controller: turn towards the (crowd
// adjusted) bearing, accelerate while closing in and brake once inside
// standoff range, and only shoot when the nose is genuinely lined up -
// the guns fire along the ship's facing, like a player's, so firing
// regardless of where it points would send bullets off at a visible angle
// to the ship.
func (s *npcSimulator) ruleDecision(view shipView) shipCommand {
	heading := s.attackHeading(view)
	dodge, dodging := s.dodgeHeading(view)
	if dodging {
		heading = dodge
	}

	step := clientSpeedRotation * s.frameScale
	turn := 0.0
	if step > 0 {
		turn = clampUnit(angleDifference(float64(view.self.Rotate), heading) / step)
	}

	// Always moving. Braking to a standstill in front of a target used to
	// be how a ship held its distance, and it made it a stationary, and so
	// trivially hittable, object; a ship that keeps its speed up is both
	// harder to hit and closer to how the game is actually played.
	//
	// Full throttle to close the distance or to get out of the way of a
	// bullet; a slow fighting speed once in range, which is what lets it
	// turn tightly enough to stay there.
	thrust := 1.0
	if !dodging && view.dist <= enemyShipStandoff*enemyShipBreakoffRange {
		thrust = -1.0
		if view.self.Speed <= s.maxSpeed*enemyShipFightSpeedFactor {
			thrust = 1.0
		}
	}

	fire := view.dist <= enemyShipShootRange &&
		math.Abs(angleDifference(float64(view.self.Rotate), view.bearing)) <= aimTolerance(view.target.radius, view.dist)

	return shipCommand{turn: turn, thrust: thrust, fire: fire}
}

// advanceAttackPhase runs the attack cycle: a ship flies an attack run
// rather than hovering, so it alternates between lining up on its target
// (nose on it, shooting) and breaking away (nose off it, repositioning for
// the next pass).
//
// Neither half is optional. The guns fire along the heading, so a ship can
// only shoot while pointing at its target, and the aim is narrow enough
// that it has to hold that point for a beat to land anything - which means
// holding a course, which is exactly what makes it easy to shoot back at.
// The cycle gives up most of the rate of fire a parked turret would have
// in exchange for never being one.
//
// The state lives on the ship (with hysteresis, like fleeing) so a pass
// lasts until it is done instead of flickering on a threshold.
func (s *npcSimulator) advanceAttackPhase(view shipView) {
	self := view.self

	switch {
	// Knife range: get out, whatever phase it was in. Contact damage is
	// mutual and ramming is not the plan.
	case view.dist <= enemyShipStandoff*enemyShipTooCloseRange:
		s.beginBreakoff(view)
	// Too far out to be fighting at all: close the distance, nose on the
	// target, which is also the half of the cycle it shoots in.
	case view.dist > enemyShipStandoff*enemyShipBreakoffRange:
		self.breakingAway = false
		self.phaseUntil = time.Time{}
	// In the fight: alternate on a timer, so the ship is never predictable
	// for long and never parked.
	case view.now.After(self.phaseUntil):
		if self.breakingAway {
			self.breakingAway = false
			self.phaseUntil = view.now.Add(self.phaseDuration(enemyShipAimPhase))
		} else {
			s.beginBreakoff(view)
		}
	}
}

// attackHeading is where the ship wants to point when nobody is shooting
// at it: at its target while it lines up, out and away (weaving) while it
// breaks off. See advanceAttackPhase for which of the two it is doing.
func (s *npcSimulator) attackHeading(view shipView) float64 {
	self := view.self
	if !self.breakingAway {
		return view.desired
	}
	return normalizeAngle(view.bearing + self.breakoffSide*enemyShipBreakoffAngle + s.weave(self))
}

// weave is the wobble a ship puts on its escape heading, in radians. Each
// ship has its own place in the cycle and its own rate through it, so a
// fleet breaking away together does not do it in formation.
func (s *npcSimulator) weave(self *enemyShipState) float64 {
	if self.weaveRate == 0 {
		self.startWeave()
	}
	self.weavePhase = math.Mod(self.weavePhase+s.weaveStep*self.weaveRate, 2*math.Pi)
	return enemyShipWeaveAngle * math.Sin(self.weavePhase)
}

// beginBreakoff starts the repositioning half of the cycle. The side is
// chosen once, on entry: re-deciding it every tick let a ship flip between
// the two while its nose was on the target, which averaged out to flying
// straight through it.
func (s *npcSimulator) beginBreakoff(view shipView) {
	self := view.self
	if !self.breakingAway {
		self.breakoffSide = math.Copysign(1, angleDifference(float64(self.Rotate), view.bearing))
		self.breakingAway = true
		self.phaseUntil = view.now.Add(self.phaseDuration(enemyShipMovePhase))
	}
}

// phaseDuration spreads the length of a phase around its nominal value, so
// a fleet attacking the same player does not fall into step and swing back
// and forth as one body.
func (e *enemyShipState) phaseDuration(base time.Duration) time.Duration {
	return base + time.Duration(e.phaseSkew*float64(enemyShipPhaseJitter)/2)
}

// dodgeHeading decides whether the ship should abandon what it is doing to
// get out of the way of a bullet, and where to.
//
// The way out is across the bullet's path, never along it: a ship can only
// move where it is pointing, so running from a bullet that travels three
// times its top speed only delays the hit, while a step sideways leaves the
// corridor entirely. Of the two sides it takes the one it is already
// closest to facing - the turn is the expensive part - and holds the choice
// for enemyShipDodgeCommit so the manoeuvre is actually completed.
func (s *npcSimulator) dodgeHeading(view shipView) (float64, bool) {
	self := view.self
	if !self.evadeUntil.IsZero() && view.now.Before(self.evadeUntil) {
		return self.evadeAngle, true
	}
	if !view.threatFound {
		return 0, false
	}

	across := normalizeAngle(view.threatAngle + math.Pi/2)
	if math.Abs(angleDifference(float64(self.Rotate), across)) >
		math.Abs(angleDifference(float64(self.Rotate), normalizeAngle(view.threatAngle-math.Pi/2))) {
		across = normalizeAngle(view.threatAngle - math.Pi/2)
	}

	self.evadeAngle = across
	self.evadeUntil = view.now.Add(enemyShipDodgeCommit)
	return across, true
}

// applyCommand executes a brain's decision: it is the only place a ship's
// heading, speed and gun are touched, so no brain can exceed the envelope
// (one tick of rotation, one tick of acceleration, one shot per cooldown).
func (s *npcSimulator) applyCommand(enemyShip *enemyShipState, command shipCommand, now time.Time) {
	turn := clampUnit(command.turn)
	enemyShip.Rotate = float32(normalizeAngle(float64(enemyShip.Rotate) + turn*clientSpeedRotation*s.frameScale))

	switch thrust := clampUnit(command.thrust); {
	case thrust > 0:
		enemyShip.Speed = math.Min(enemyShip.Speed+s.accel*thrust, s.maxSpeed)
	case thrust < 0:
		// thrust is negative here, so this subtracts.
		enemyShip.Speed = math.Max(enemyShip.Speed+s.decel*thrust, 0)
	}
	s.advance(enemyShip)

	if command.fire && now.Sub(enemyShip.lastShotAt) > s.settings.fireCooldown() {
		s.fireAt(enemyShip, now)
		enemyShip.lastShotAt = now
	}
}

func clampUnit(value float64) float64 {
	return math.Max(-1, math.Min(1, value))
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
// ramEnemyShips damages every pair of enemy ships that are touching. It is
// the NPC-vs-NPC half of ramming: ships-vue resolves a *player's* contacts
// (its own damage, and via npcHit the damage to whatever it rammed), but no
// browser is watching two NPCs collide, so those hits are settled here.
//
// The point is parity. A player who rams an NPC takes damage, so the NPC has
// to take it too, and two NPCs colliding must be no safer than a player
// colliding with one - otherwise NPCs get a free advantage nobody else has.
//
// Rate-limited per pair with the same cooldown a browser uses, so grinding
// along another hull costs the same as it does for a player rather than
// draining a ship in a few ticks. Both ships are damaged: the contact is
// mutual and each is credited to the other, exactly as it would be if two
// players had done it from their own browsers.
func (s *npcSimulator) ramEnemyShips(now time.Time) {
	if !s.settings.ContactDamage {
		return
	}
	// Expired entries are dropped rather than kept keyed by a pair that may
	// never touch again: with 100 v 100 ships that map would otherwise grow
	// towards 20k entries and never shrink.
	for key, at := range s.ramHitAt {
		if now.Sub(at) >= enemyShipRamCooldown {
			delete(s.ramHitAt, key)
		}
	}

	ships := s.sortedEnemyShips()
	// Collected first: damageEnemyShip can retire a ship, and a kill would
	// otherwise mutate s.enemyShips while it is being walked.
	type ramPair struct{ a, b *enemyShipState }
	var pairs []ramPair
	for i := 0; i < len(ships); i++ {
		for j := i + 1; j < len(ships); j++ {
			a, b := ships[i], ships[j]
			if !shipsOverlap(a, b) {
				continue
			}
			key := a.Id + "|" + b.Id
			if _, cooling := s.ramHitAt[key]; cooling {
				continue
			}
			s.ramHitAt[key] = now
			pairs = append(pairs, ramPair{a, b})
		}
	}
	for _, pair := range pairs {
		// Re-checked: an earlier pair in this same tick may already have
		// killed one of these two, and a dead ship neither deals nor takes
		// damage.
		if pair.a.Life <= 0 || pair.b.Life <= 0 {
			continue
		}
		s.damageEnemyShip(pair.a, "", pair.b.Id, enemyShipRamDamage)
		s.damageEnemyShip(pair.b, "", pair.a.Id, enemyShipRamDamage)
	}
}

// shipsOverlap reports whether two enemy ships' hulls intersect, using the
// same axis-aligned test ships-vue uses for a player.
func shipsOverlap(a, b *enemyShipState) bool {
	aw, ah := a.ship.size()
	bw, bh := b.ship.size()
	return math.Min(a.X+aw, b.X+bw) > math.Max(a.X, b.X) &&
		math.Min(a.Y+ah, b.Y+bh) > math.Max(a.Y, b.Y)
}

// sortedEnemyShips returns the fleet in a stable order, so anything
// resolving pairs gives the same result run to run despite Go's randomised
// map iteration.
func (s *npcSimulator) sortedEnemyShips() []*enemyShipState {
	ships := make([]*enemyShipState, 0, len(s.enemyShips))
	for _, ship := range s.enemyShips {
		ships = append(ships, ship)
	}
	sort.Slice(ships, func(i, j int) bool { return ships[i].Id < ships[j].Id })
	return ships
}

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
	// Sorted so the resolution order (and therefore the outcome) doesn't
	// depend on Go's randomised map iteration.
	ships := s.sortedEnemyShips()

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
// thing its own fleet is allowed to attack, per the admin's attack matrix.
// A ship never targets itself, and a ship fleeing a black hole never gets
// here at all. A fleet allowed to attack nothing finds no target and
// simply coasts - it still flies, dodges black holes and can be shot.
func (s *npcSimulator) nearestTarget(self *enemyShipState, x, y float64, players map[string]PlayerData) (aimTarget, bool) {
	var best aimTarget
	bestDist := math.MaxFloat64
	found := false

	huntsPlayers := s.settings.attacks(self.controller, factionPlayers)
	for id, p := range players {
		if !huntsPlayers || p.IsDead {
			continue
		}
		px, py := s.playerCenter(p)
		d := math.Hypot(px-x, py-y)
		if !found || d < bestDist {
			best = aimTarget{id: id, x: px, y: py, radius: s.playerRadius(p)}
			bestDist, found = d, true
		}
	}

	for id, other := range s.enemyShips {
		if other == self || !s.settings.attacks(self.controller, other.controller.faction()) {
			continue
		}
		ox, oy := other.center()
		d := math.Hypot(ox-x, oy-y)
		if !found || d < bestDist {
			best = aimTarget{id: id, x: ox, y: oy, radius: other.radius(), isShip: true}
			bestDist, found = d, true
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
	s.retireDeadEnemyShip(enemyShip)
	delete(s.enemyShips, enemyShip.Id)
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
		ownerId:         enemyShip.Id,
		ownerController: enemyShip.controller,
		x:               originX,
		y:               originY,
		stepX:           moveX * npcBulletSpeed * s.frameScale,
		stepY:           moveY * npcBulletSpeed * s.frameScale,
		firedAt:         now,
	}
}

// trackIncomingBullets records the shots ships-go has just broadcast, so
// the fleet can see what is being fired at it. Called from the client's
// read goroutine.
//
// Our own fleet's bullets are skipped: they are already tracked, with the
// same trajectory, in activeBullets. Everything else is a player's.
func (s *npcSimulator) trackIncomingBullets(bullets []newBulletMsg) {
	s.mu.Lock()
	defer s.mu.Unlock()

	now := time.Now()
	for _, bullet := range bullets {
		if _, ours := s.enemyShips[bullet.SocketId]; ours {
			continue
		}
		if _, known := s.incomingBullets[bullet.Id]; known {
			continue
		}
		speed := (25*1.5 + float64(bullet.ShootingSpeed)) * s.frameScale
		s.incomingBullets[bullet.Id] = &npcBullet{
			ownerId: bullet.SocketId,
			x:       float64(bullet.X),
			y:       float64(bullet.Y),
			stepX:   float64(bullet.MoveX) * speed,
			stepY:   float64(bullet.MoveY) * speed,
			firedAt: now,
		}
	}
}

// advanceIncomingBullets flies the tracked player bullets one tick and
// forgets the ones that have travelled their range. Nothing is resolved
// here - they exist only to be avoided - so unlike our own bullets they
// are never tested against anything and no removeBullet is sent for them.
func (s *npcSimulator) advanceIncomingBullets(now time.Time) {
	for id, bullet := range s.incomingBullets {
		if now.Sub(bullet.firedAt) >= npcBulletLifetime {
			delete(s.incomingBullets, id)
			continue
		}
		bullet.x += bullet.stepX
		bullet.y += bullet.stepY
	}
}

// nearestThreat finds the bullet that will come closest to hitting this
// ship soonest, and how urgent it is.
//
// A bullet is a threat when the distance between it and the ship, at the
// point of closest approach along its path, is less than the ship's own
// radius with a margin - the same "will this segment pass through that
// circle" question the hit detection asks, asked about the future instead
// of the past.
//
// Both the fleet's own bullets and the players' are considered: a ship
// should duck a shot from an allied fleet it is allowed to be hurt by, and
// ignore one it cannot be hurt by, exactly as the damage rules say.
func (s *npcSimulator) nearestThreat(enemyShip *enemyShipState, selfX, selfY, selfRadius float64) (angle, urgency float64, found bool) {
	// In ticks, since a bullet's step is per tick: the tick interval is
	// one client frame times frameScale.
	lookahead := float64(enemyShipDodgeLookahead) / (float64(clientTickInterval) * s.frameScale)
	if lookahead <= 0 || math.IsInf(lookahead, 0) {
		return 0, 0, false
	}
	danger := selfRadius * enemyShipDodgeMargin

	consider := func(bullet *npcBullet, canHurt bool) {
		if !canHurt || bullet.ownerId == enemyShip.Id {
			return
		}
		relX, relY := bullet.x-selfX, bullet.y-selfY
		speedSq := bullet.stepX*bullet.stepX + bullet.stepY*bullet.stepY
		if speedSq == 0 {
			return
		}
		// Ticks until the bullet is at its closest to us, clamped to the
		// window: negative means it is already going away.
		t := -(relX*bullet.stepX + relY*bullet.stepY) / speedSq
		if t < 0 || t > lookahead {
			return
		}
		missBy := math.Hypot(relX+t*bullet.stepX, relY+t*bullet.stepY)
		if missBy > danger {
			return
		}
		if soonest := 1 - t/lookahead; soonest > urgency {
			angle, urgency, found = math.Atan2(bullet.stepY, bullet.stepX), soonest, true
		}
	}

	for _, bullet := range s.incomingBullets {
		consider(bullet, true)
	}
	for _, bullet := range s.activeBullets {
		consider(bullet, s.settings.attacks(bullet.ownerController, enemyShip.controller.faction()))
	}
	return angle, urgency, found
}

// advanceBullets flies this service's own bullets one tick, resolves the
// NPC-vs-NPC hits nobody else can (see activeBullets), and retires bullets
// that have had enough time to travel their full range.
//
// Bullets are always flown, even when nothing may be hit by them: a
// dormant trajectory costs nothing and it means widening the attack matrix
// takes effect on the very next tick instead of only for bullets fired
// after it.
func (s *npcSimulator) advanceBullets(now time.Time) {
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
		// A bullet only harms what its owner's fleet is allowed to attack,
		// so a stray shot passes harmlessly through an allied ship instead
		// of turning a one-sided matrix into crossfire.
		if ship.Id == bullet.ownerId || !s.settings.attacks(bullet.ownerController, ship.controller.faction()) {
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

	// A ram carries no bullet: ships-vue reports one with an empty bulletId,
	// the same convention playerHit already uses for black hole and ram
	// damage. It is the one kind of npcHit the admin can switch off, and
	// there is nothing to remove from anyone's screen for it.
	if hit.BulletId == "" {
		if !s.settings.ContactDamage {
			return
		}
		s.damageEnemyShip(enemyShip, "", hit.From, hit.BulletCharge)
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

	s.retireDeadEnemyShip(enemyShip)
	delete(s.enemyShips, enemyShip.Id)
}

// spawnEnemyShip creates a brand new ship for one fleet. The name carries
// the fleet's tag ([NPC] or [AI]) and a per-fleet number, so a hundred
// ships are individually identifiable on screen and in the scoreboard
// rather than a hundred repetitions of the same hull name.
func (s *npcSimulator) spawnEnemyShip(controller controllerKind, players map[string]PlayerData, now time.Time) *enemyShipState {
	x, y := s.spawnPoint(players)
	ship := s.ships[rand.Intn(len(s.ships))]
	s.spawnCount[controller]++
	maxLife := s.settings.ShipLife

	spawned := &enemyShipState{
		spawnedAt:  now,
		controller: controller,
		ship:       ship,
		NpcData: NpcData{
			Type:   NpcTypes.Ship,
			Id:     uuid.NewString(),
			ShipId: ship.Id,
			Name:   fmt.Sprintf("%s %s %d", controller.namePrefix(), ship.Name, s.spawnCount[controller]),
			X:      x,
			Y:      y,
			// A random heading, so a fleet doesn't appear all facing the
			// same way and each ship has a different distance to turn
			// before it can open fire.
			Rotate:  float32(rand.Float64() * 2 * math.Pi),
			Scale:   s.shipScale(ship),
			Life:    maxLife,
			MaxLife: maxLife,
		},
	}
	spawned.startWeave()
	return spawned
}

// spawnPoint picks somewhere for a new enemy ship: inside a box that grows
// with the fleet, and with elbow room from everything already on the map.
//
// Both halves matter. A box sized for one ship puts two hundred on top of
// each other however carefully each point is chosen, and even in a large box
// uniform random points clump - "random" is not "evenly spread", so ships
// arrive in clusters with gaps between them. So the box is sized from the
// fleet, and points too close to an existing ship or a player are rejected.
// Ships still converge on the players within seconds; this is only about not
// materialising in a pile.
func (s *npcSimulator) spawnPoint(players map[string]PlayerData) (float64, float64) {
	minX, minY, maxX, maxY := spawnBounds(players, s.spawnMargin())
	rangeX := float64(maxX - minX)
	rangeY := float64(maxY - minY)

	bestX, bestY, bestGap := 0.0, 0.0, -1.0
	for try := 0; try < enemyShipSpawnTries; try++ {
		x := rand.Float64()*rangeX + float64(minX)
		y := rand.Float64()*rangeY + float64(minY)
		gap := s.nearestOccupant(x, y, players)
		if gap >= enemyShipSpawnSpacing {
			return x, y
		}
		if gap > bestGap {
			bestX, bestY, bestGap = x, y, gap
		}
	}
	return bestX, bestY
}

// spawnMargin is how far out from the players the spawn box reaches. It is
// sized so the whole fleet has enemyShipSpawnSpacing of room each: the area
// needed grows with the number of ships, so the box has to grow with it or
// the spacing above is unsatisfiable and every spawn falls back to
// best-of-N in a box that is simply too small.
func (s *npcSimulator) spawnMargin() float32 {
	fleet := s.settings.fleetSize(controllerRule) + s.settings.fleetSize(controllerAi)
	if fleet < 1 {
		fleet = 1
	}
	// side of a square giving every ship its own spacing x spacing cell
	side := math.Sqrt(float64(fleet)) * enemyShipSpawnSpacing
	return float32(math.Max(enemyShipSpawnMargin, side/2))
}

// nearestOccupant is the distance from a candidate spawn point to the
// closest thing already on the map - an enemy ship of either fleet, or a
// player. Players count: dropping a fresh ship in someone's lap is worse
// than dropping it next to another NPC.
func (s *npcSimulator) nearestOccupant(x, y float64, players map[string]PlayerData) float64 {
	// Measured anchor to anchor, the coordinate everything travels as on
	// the wire. Centers would be more precise, but the candidate has no
	// ship (and so no size) yet, and half a hull is noise next to a
	// spacing of several hundred pixels.
	nearest := math.Inf(1)
	for _, ship := range s.enemyShips {
		nearest = math.Min(nearest, math.Hypot(ship.X-x, ship.Y-y))
	}
	for _, p := range players {
		nearest = math.Min(nearest, math.Hypot(float64(p.X)-x, float64(p.Y)-y))
	}
	return nearest
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

// blackHoleScaleAt is the size of a black hole at a given point in its
// life: it opens over blackHoleGrowthTime, stays full, then collapses over
// the same time so that it is gone exactly when its configured duration is
// up. Deriving it from elapsed time rather than adding a step per tick
// keeps it identical whatever the tick rate, and makes the admin duration
// mean the whole visible life instead of just the part before the collapse.
func blackHoleScaleAt(elapsed, duration time.Duration) float64 {
	growth := min(blackHoleGrowthTime, duration/3)
	if growth <= 0 {
		return 1.0
	}

	scale := 1.0
	switch {
	case elapsed < growth:
		scale = float64(elapsed) / float64(growth)
	case elapsed > duration-growth:
		scale = float64(duration-elapsed) / float64(growth)
	}
	return math.Max(blackHoleScaleStep, math.Min(1.0, scale))
}

func spawnBlackHole(players map[string]PlayerData, now time.Time, duration time.Duration) *npcState {
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
			Duration:  int(duration.Milliseconds()),
			Speed:     blackHoleSpeed,
		},
	}
}
