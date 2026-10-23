package game

import (
	"sort"
	"strconv"
	"strings"
	"time"

	"tap/internal/world"
)

// Combat tuning. See the README "Combat System" section for the rationale.
const (
	unarmedDamage = 5
	critChance    = 0.10
	npcMissChance = 0.15
	fleeChance    = 0.60
)

type combatResult struct {
	Attacker      string   `json:"attacker,omitempty"`
	AttackerHP    int      `json:"attacker_hp"`
	TargetHP      int      `json:"target_hp"`
	Damage        int      `json:"damage"`
	Status        string   `json:"status"`
	Action        string   `json:"action"`
	Target        string   `json:"target"`
	TargetMaxHP   int      `json:"target_max_hp"`
	Critical      bool     `json:"critical,omitempty"`
	CounterDamage int      `json:"counter_damage"`
	Item          string   `json:"item,omitempty"`
	Healed        int      `json:"healed,omitempty"`
	Loot          []string `json:"loot,omitempty"`
	Room          string   `json:"room,omitempty"`
}

func (g *Game) bestStat(p *Player, stat func(*world.Item) int, categories ...string) int {
	best := 0
	for _, id := range p.inventory {
		it := g.w.Items[id]
		if len(categories) > 0 && !containsFold(categories, it.Category) {
			continue
		}
		if v := stat(it); v > best {
			best = v
		}
	}
	return best
}

func containsFold(list []string, s string) bool {
	for _, v := range list {
		if strings.EqualFold(v, s) {
			return true
		}
	}
	return false
}

// playerDamage: (5 + best weapon damage) plus up to 50% random bonus,
// doubled on a critical hit.
func (g *Game) playerDamage(p *Player) (int, bool) {
	base := unarmedDamage + g.bestStat(p, func(it *world.Item) int { return it.Damage })
	dmg := base + g.rng.Intn(base/2+1)
	crit := g.rng.Float64() < critChance
	if crit {
		dmg *= 2
	}
	return dmg, crit
}

// defense: best shield + best armor carried.
func (g *Game) defense(p *Player) int {
	stat := func(it *world.Item) int { return it.Defense }
	return g.bestStat(p, stat, "Shield") + g.bestStat(p, stat, "Armor")
}

// npcStrike applies one enemy attack to p and returns the damage dealt.
// Defending halves the raw damage and doubles the defense.
func (g *Game) npcStrike(n *npcState, p *Player, defending bool) int {
	if g.rng.Float64() < npcMissChance {
		return 0
	}
	base := n.def.Stats.Damage + g.rng.Intn(n.def.Stats.Damage/4+1)
	def := g.defense(p)
	if defending {
		base /= 2
		def *= 2
	}
	dmg := max(1, base-def)
	p.hp = max(0, p.hp-dmg)
	return dmg
}

// currentTarget returns the enemy p is fighting, clearing stale state.
func (g *Game) currentTarget(p *Player) *npcState {
	if p.target == "" {
		return nil
	}
	n := g.npcs[p.target]
	if n == nil || !n.alive || n.def.Room != p.room {
		old := p.target
		p.target = ""
		g.resetIfDisengaged(old)
		return nil
	}
	return n
}

// resetIfDisengaged heals an enemy back to full once nobody fights it.
func (g *Game) resetIfDisengaged(npcID string) {
	n := g.npcs[npcID]
	if n == nil || !n.alive || n.def.Stats == nil {
		return
	}
	for _, p := range g.players {
		if p.target == npcID {
			return
		}
	}
	if n.hp != n.def.Stats.MaxHP {
		n.hp = n.def.Stats.MaxHP
		g.log.Info("npc reset", "type", "combat", "npc", npcID)
	}
}

func (g *Game) cmdAttack(p *Player, args string, o *outbox) string {
	if args == "" {
		return errInvalidArgs
	}
	id, ok := resolve(args, g.rooms[p.room].npcs, g.npcName)
	if !ok {
		return errNPCNotFound
	}
	n := g.npcs[id]
	if n.def.Role != world.RoleEnemy {
		return errNPCNotHostile
	}
	if p.target != "" && p.target != id {
		old := p.target
		p.target = id
		g.resetIfDisengaged(old)
	}
	p.target = id
	p.defending = false

	res := combatResult{Action: "attack", Target: id, TargetMaxHP: n.def.Stats.MaxHP}
	res.Damage, res.Critical = g.playerDamage(p)
	n.hp = max(0, n.hp-res.Damage)
	res.TargetHP = n.hp
	if n.hp == 0 {
		res.Status = "victory"
		res.Loot = g.defeatNPC(n, p, o)
	} else {
		res.Status = "combat"
		res.CounterDamage = g.npcStrike(n, p, false)
	}
	return g.finishRound(p, &res, o)
}

func (g *Game) cmdDefend(p *Player, args string, o *outbox) string {
	if args != "" {
		return errInvalidArgs
	}
	n := g.currentTarget(p)
	if n == nil {
		return errNotInCombat
	}
	p.defending = true
	res := combatResult{Action: "defend", Target: n.id, TargetHP: n.hp, TargetMaxHP: n.def.Stats.MaxHP, Status: "combat"}
	res.CounterDamage = g.npcStrike(n, p, true)
	return g.finishRound(p, &res, o)
}

func (g *Game) cmdFlee(p *Player, args string, o *outbox) string {
	if args != "" {
		return errInvalidArgs
	}
	n := g.currentTarget(p)
	if n == nil {
		return errNotInCombat
	}
	res := combatResult{Action: "flee", Target: n.id, TargetHP: n.hp, TargetMaxHP: n.def.Stats.MaxHP}
	exits := g.rooms[p.room].def.Exits
	dirs := make([]string, 0, len(exits))
	for d := range exits {
		dirs = append(dirs, d)
	}
	sort.Strings(dirs)
	if len(dirs) > 0 && g.rng.Float64() < fleeChance {
		dest := exits[dirs[g.rng.Intn(len(dirs))]]
		res.Status = "fled"
		res.Room = dest
		res.AttackerHP = p.hp
		g.broadcastRound(p, &res, o)
		p.target = ""
		p.defending = false
		g.resetIfDisengaged(n.id)
		g.movePlayer(p, dest, o)
		return "OK " + mustJSON(res)
	}
	res.Status = "combat"
	res.CounterDamage = g.npcStrike(n, p, false)
	return g.finishRound(p, &res, o)
}

func (g *Game) cmdUse(p *Player, args string, o *outbox) string {
	if args == "" {
		return errInvalidArgs
	}
	id, ok := resolve(args, p.inventory, g.itemName)
	if !ok {
		return errItemNotInInv
	}
	it := g.w.Items[id]
	if it.Heal <= 0 {
		return errNotUsable
	}
	p.inventory = removeFirst(p.inventory, id)
	before := p.hp
	p.hp = min(p.maxHP, p.hp+it.Heal)
	res := combatResult{Action: "use", Item: id, Healed: p.hp - before}
	g.log.Info("item used", "type", "world", "player", p.Name, "item", id, "healed", res.Healed)
	if n := g.currentTarget(p); n != nil {
		res.Target, res.TargetHP, res.TargetMaxHP = n.id, n.hp, n.def.Stats.MaxHP
		res.Status = "combat"
		res.CounterDamage = g.npcStrike(n, p, false)
		g.updateQuests(p, "", o)
		return g.finishRound(p, &res, o)
	}
	res.Status = healthLabel(p)
	res.AttackerHP = p.hp
	g.updateQuests(p, "", o)
	return "OK " + mustJSON(res)
}

// finishRound handles player death, logs and broadcasts the round, and
// returns the reply line.
func (g *Game) finishRound(p *Player, res *combatResult, o *outbox) string {
	res.AttackerHP = p.hp
	if p.hp <= 0 {
		res.Status = "defeated"
	}
	g.broadcastRound(p, res, o)
	if p.hp <= 0 {
		g.respawnPlayer(p, o)
		res.AttackerHP = p.hp
		res.Room = p.room
	}
	return "OK " + mustJSON(res)
}

func (g *Game) broadcastRound(p *Player, res *combatResult, o *outbox) {
	g.log.Info("combat round", "type", "combat", "player", p.Name, "room", p.room, "action", res.Action,
		"target", res.Target, "damage", res.Damage, "critical", res.Critical, "counter_damage", res.CounterDamage,
		"player_hp", res.AttackerHP, "target_hp", res.TargetHP, "status", res.Status)
	ev := *res
	ev.Attacker = p.Name
	g.toRoom(o, p.room, p, "EVT ROOM COMBAT "+mustJSON(ev))
}

// respawnPlayer sends a defeated player to the safe room with reduced HP.
func (g *Game) respawnPlayer(p *Player, o *outbox) {
	target := p.target
	p.target = ""
	p.defending = false
	p.hp = RespawnHP
	g.log.Info("player defeated", "type", "combat", "player", p.Name, "room", p.room, "killer", target, "respawn", g.w.SafeRoom)
	if p.room != g.w.SafeRoom {
		g.movePlayer(p, g.w.SafeRoom, o)
	}
	o.to(p, "EVT PLAYER RESPAWN room="+g.w.SafeRoom+" hp="+strconv.Itoa(p.hp))
	g.resetIfDisengaged(target)
}

// partingStrike: leaving a fight with MOVE gives the enemy one free, never
// lethal, attack.
func (g *Game) partingStrike(p *Player, o *outbox) {
	n := g.currentTarget(p)
	if n == nil {
		return
	}
	dmg := g.npcStrike(n, p, false)
	if p.hp == 0 {
		p.hp = 1
	}
	p.target = ""
	p.defending = false
	g.resetIfDisengaged(n.id)
	g.log.Info("parting strike", "type", "combat", "player", p.Name, "npc", n.id, "damage", dmg, "player_hp", p.hp)
	o.to(p, "EVT COMBAT HIT "+mustJSON(map[string]any{"npc": n.id, "damage": dmg, "hp": p.hp, "reason": "parting_strike"}))
}

// defeatNPC removes the enemy, drops its loot, gives quest credit to every
// fighter (and their group mates in the room) and schedules a respawn.
func (g *Game) defeatNPC(n *npcState, killer *Player, o *outbox) []string {
	n.alive = false
	n.hp = 0
	room := g.rooms[n.def.Room]
	room.npcs = removeFirst(room.npcs, n.id)
	loot := append([]string(nil), n.def.Drops...)
	room.items = append(room.items, loot...)

	credited := map[*Player]bool{killer: true}
	for _, p := range room.players {
		if p.target == n.id {
			credited[p] = true
		}
	}
	for p := range credited {
		if p.group != nil {
			for _, m := range p.group.Members {
				if m.room == room.id {
					credited[m] = true
				}
			}
		}
	}
	for _, p := range room.players {
		if p.target == n.id {
			p.target = ""
			p.defending = false
		}
	}
	g.log.Info("npc defeated", "type", "combat", "npc", n.id, "room", room.id, "killer", killer.Name, "loot", loot)
	g.toRoom(o, room.id, killer, "EVT ROOM NPC DEFEATED "+n.id+" "+killer.Name)
	for p := range credited {
		g.creditDefeat(p, n.id, o)
	}

	id := n.id
	time.AfterFunc(g.respawnDelay, func() { g.respawnNPC(id) })
	return loot
}

func (g *Game) respawnNPC(id string) {
	g.mu.Lock()
	defer g.mu.Unlock()
	n := g.npcs[id]
	if n.alive {
		return
	}
	n.alive = true
	n.hp = n.def.Stats.MaxHP
	room := g.rooms[n.def.Room]
	room.npcs = append(room.npcs, id)
	g.log.Info("npc respawned", "type", "world", "npc", id, "room", room.id)
	var o outbox
	g.toRoom(&o, room.id, nil, "EVT ROOM NPC SPAWN "+id)
	g.flush(&o)
}

func healthLabel(p *Player) string {
	switch {
	case p.hp*10 >= p.maxHP*7:
		return "healthy"
	case p.hp*10 >= p.maxHP*3:
		return "wounded"
	default:
		return "critical"
	}
}

type statusReply struct {
	HP       int    `json:"hp"`
	MaxHP    int    `json:"max_hp"`
	Status   string `json:"status"`
	InCombat bool   `json:"in_combat"`
	Target   string `json:"target,omitempty"`
	TargetHP *int   `json:"target_hp,omitempty"`
	Room     string `json:"room"`
}

func (g *Game) cmdStatus(p *Player, args string, _ *outbox) string {
	if args != "" {
		return errInvalidArgs
	}
	rep := statusReply{HP: p.hp, MaxHP: p.maxHP, Status: healthLabel(p), Room: p.room}
	if n := g.currentTarget(p); n != nil {
		hp := n.hp
		rep.Status, rep.InCombat, rep.Target, rep.TargetHP = "combat", true, n.id, &hp
	}
	return "OK " + mustJSON(rep)
}
