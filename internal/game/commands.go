package game

import (
	"strconv"
	"strings"
	"unicode"
)

// ---------------------------------------------------------------------------
// Name resolution: items and NPCs may be referenced by canonical ID
// ("item.healing_herbs"), by ID without prefix ("healing_herbs"), by display
// name ("Healing Herbs", any case) or by words of the display name ("herbs").

func normalize(s string) string {
	s = strings.ToLower(strings.TrimSpace(s))
	s = strings.Trim(s, `"'`)
	s = strings.ReplaceAll(s, "_", " ")
	return strings.Join(strings.Fields(s), " ")
}

func idSuffix(id string) string {
	if i := strings.IndexByte(id, '.'); i >= 0 {
		return id[i+1:]
	}
	return id
}

func matchScore(query, id, name string) int {
	q := normalize(query)
	if q == "" {
		return 0
	}
	if strings.EqualFold(strings.TrimSpace(query), id) {
		return 3
	}
	n := normalize(name)
	suf := normalize(idSuffix(id))
	if q == n || q == suf {
		return 2
	}
	words := strings.Fields(n + " " + suf)
	for _, qw := range strings.Fields(q) {
		found := false
		for _, w := range words {
			if w == qw {
				found = true
				break
			}
		}
		if !found {
			return 0
		}
	}
	return 1
}

// resolve returns the candidate ID best matching query.
func resolve(query string, ids []string, name func(string) string) (string, bool) {
	best, bestScore := "", 0
	for _, id := range ids {
		if s := matchScore(query, id, name(id)); s > bestScore {
			best, bestScore = id, s
		}
	}
	return best, bestScore > 0
}

func (g *Game) itemName(id string) string {
	if it, ok := g.w.Items[id]; ok {
		return it.Name
	}
	return id
}

func (g *Game) npcName(id string) string {
	if n, ok := g.w.NPCs[id]; ok {
		return n.Name
	}
	return id
}

func removeFirst(list []string, s string) []string {
	for i, v := range list {
		if v == s {
			return append(list[:i:i], list[i+1:]...)
		}
	}
	return list
}

func sanitize(s string) string {
	return strings.Map(func(r rune) rune {
		if unicode.IsControl(r) {
			return -1
		}
		return r
	}, s)
}

// ---------------------------------------------------------------------------
// LOOK / MOVE / WHO / CHAT

type lookRoom struct {
	ID          string            `json:"id"`
	Name        string            `json:"name"`
	Description string            `json:"description"`
	Exits       map[string]string `json:"exits"`
}

type lookReply struct {
	Room    lookRoom `json:"room"`
	Players []string `json:"players"`
	Items   []string `json:"items"`
	NPCs    []string `json:"npcs"`
}

func (g *Game) cmdLook(p *Player, args string, _ *outbox) string {
	if args != "" {
		return errInvalidArgs
	}
	r := g.rooms[p.room]
	exits := r.def.Exits
	if exits == nil {
		exits = map[string]string{}
	}
	rep := lookReply{
		Room:    lookRoom{ID: r.id, Name: r.def.Name, Description: r.def.Description, Exits: exits},
		Players: make([]string, 0, len(r.players)),
		Items:   append([]string{}, r.items...),
		NPCs:    append([]string{}, r.npcs...),
	}
	for _, q := range r.players {
		rep.Players = append(rep.Players, q.Name)
	}
	return "OK " + mustJSON(rep)
}

var dirAliases = map[string]string{
	"n": "north", "s": "south", "e": "east", "w": "west", "u": "up", "d": "down",
	"ne": "northeast", "nw": "northwest", "se": "southeast", "sw": "southwest",
}

func (g *Game) cmdMove(p *Player, args string, o *outbox) string {
	dir := strings.ToLower(args)
	if dir == "" || strings.ContainsAny(dir, " \t") {
		return errInvalidArgs
	}
	if full, ok := dirAliases[dir]; ok {
		dir = full
	}
	target, ok := g.rooms[p.room].def.Exits[dir]
	if !ok {
		return errNoExit
	}
	if p.target != "" {
		g.partingStrike(p, o)
	}
	g.movePlayer(p, target, o)
	return "OK room=" + target
}

func (g *Game) movePlayer(p *Player, target string, o *outbox) {
	old := p.room
	g.rooms[old].players = removePlayer(g.rooms[old].players, p)
	g.toRoom(o, old, p, "EVT ROOM PRESENCE LEAVE "+p.Name)
	g.toRoom(o, target, p, "EVT ROOM PRESENCE ENTER "+p.Name)
	p.room = target
	g.rooms[target].players = append(g.rooms[target].players, p)
	g.log.Info("player moved", "type", "world", "player", p.Name, "from", old, "to", target)
}

func (g *Game) cmdWho(_ *Player, args string, _ *outbox) string {
	if args != "" {
		return errInvalidArgs
	}
	return "OK players=" + strconv.Itoa(len(g.players))
}

func (g *Game) cmdChat(p *Player, args string, o *outbox) string {
	scope, msg, _ := strings.Cut(args, " ")
	msg = strings.TrimSpace(sanitize(msg))
	if msg == "" {
		return errInvalidArgs
	}
	switch strings.ToUpper(scope) {
	case "GLOBAL":
		g.toAll(o, "EVT GLOBAL CHAT "+p.Name+" "+msg)
	case "ROOM":
		g.toRoom(o, p.room, nil, "EVT ROOM CHAT "+p.Name+" "+msg)
	case "GROUP":
		if p.group == nil {
			return errNotInGroup
		}
		for _, m := range p.group.Members {
			o.to(m, "EVT GROUP CHAT "+p.Name+" "+msg)
		}
	default:
		return errInvalidArgs
	}
	g.log.Info("chat", "type", "chat", "player", p.Name, "scope", strings.ToUpper(scope), "length", len(msg))
	return "OK"
}

// ---------------------------------------------------------------------------
// Items

func (g *Game) cmdTake(p *Player, args string, o *outbox) string {
	if args == "" {
		return errInvalidArgs
	}
	r := g.rooms[p.room]
	id, ok := resolve(args, r.items, g.itemName)
	if !ok {
		return errItemNotFound
	}
	if !g.w.Items[id].Obtainable {
		return errNotObtainable
	}
	if len(p.inventory) >= MaxInventory {
		return errInventoryFull
	}
	r.items = removeFirst(r.items, id)
	p.inventory = append(p.inventory, id)
	g.log.Info("item taken", "type", "world", "player", p.Name, "item", id, "room", r.id)
	g.toRoom(o, r.id, p, "EVT ROOM ITEM TAKEN "+p.Name+" "+id)
	g.updateQuests(p, "", o)
	return "OK taken=" + id
}

func (g *Game) cmdDrop(p *Player, args string, o *outbox) string {
	if args == "" {
		return errInvalidArgs
	}
	id, ok := resolve(args, p.inventory, g.itemName)
	if !ok {
		return errItemNotInInv
	}
	p.inventory = removeFirst(p.inventory, id)
	r := g.rooms[p.room]
	r.items = append(r.items, id)
	g.log.Info("item dropped", "type", "world", "player", p.Name, "item", id, "room", r.id)
	g.toRoom(o, r.id, p, "EVT ROOM ITEM DROPPED "+p.Name+" "+id)
	g.updateQuests(p, "", o)
	return "OK dropped=" + id
}

func (g *Game) cmdInventory(p *Player, args string, _ *outbox) string {
	if args != "" {
		return errInvalidArgs
	}
	return "OK " + mustJSON(append([]string{}, p.inventory...))
}

// ---------------------------------------------------------------------------
// NPC dialogue

func (g *Game) cmdTalk(p *Player, args string, o *outbox) string {
	if args == "" {
		return errInvalidArgs
	}
	id, ok := resolve(args, g.rooms[p.room].npcs, g.npcName)
	if !ok {
		return errNPCNotFound
	}
	n := g.w.NPCs[id]
	line := n.Name + " says nothing."
	if len(n.Dialogue) > 0 {
		line = n.Dialogue[p.talks[id]%len(n.Dialogue)]
		p.talks[id]++
	}
	g.log.Info("npc talk", "type", "npc", "player", p.Name, "npc", id)
	g.updateQuests(p, id, o)
	return "OK " + sanitize(line)
}
