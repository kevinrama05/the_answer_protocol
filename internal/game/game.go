// Package game holds the shared world state and implements every TAP
// command. All state is protected by a single mutex: a command runs as one
// critical section, so the world is always seen in a consistent state.
package game

import (
	"encoding/json"
	"log/slog"
	"math/rand"
	"strconv"
	"strings"
	"sync"
	"time"
	"unicode"
	"unicode/utf8"

	"tap/internal/world"
)

// Sink receives lines destined to one client. Send must never block: the
// network layer queues the line and writes it from its own goroutine.
type Sink interface {
	Send(line string)
}

// Protocol limits.
const (
	MaxNameLen     = 24
	MaxInventory   = 20
	PlayerMaxHP    = 100
	RespawnHP      = 50
	DefaultRespawn = 60 * time.Second
)

// Player is the state of one authenticated client.
type Player struct {
	Name      string
	Addr      string
	sink      Sink
	room      string
	hp        int
	maxHP     int
	inventory []string
	group     *Group
	invites   map[string]bool // group IDs the player was invited to
	target    string          // enemy NPC the player is fighting, "" if none
	defending bool
	quests    map[string]*questState
	questLog  []string // quest IDs in acceptance order
	talks     map[string]int
}

// Group is a set of players chatting and sharing quest kill credit.
type Group struct {
	ID      string
	Leader  *Player
	Members []*Player
}

type npcState struct {
	id    string
	def   *world.NPC
	hp    int
	alive bool
}

type roomState struct {
	id      string
	def     *world.Room
	items   []string
	players []*Player
	npcs    []string
}

// Game is the whole mutable world.
type Game struct {
	mu           sync.Mutex
	w            *world.World
	log          *slog.Logger
	rng          *rand.Rand
	respawnDelay time.Duration

	rooms    map[string]*roomState
	npcs     map[string]*npcState
	players  map[string]*Player // keyed by lower-cased name
	groups   map[string]*Group
	groupSeq int
}

// Options tunes a Game. Zero values select defaults.
type Options struct {
	Seed         int64
	RespawnDelay time.Duration
}

// New builds the runtime state from a validated world.
func New(w *world.World, log *slog.Logger, opt Options) *Game {
	if opt.Seed == 0 {
		opt.Seed = time.Now().UnixNano()
	}
	if opt.RespawnDelay == 0 {
		opt.RespawnDelay = DefaultRespawn
	}
	g := &Game{
		w:            w,
		log:          log,
		rng:          rand.New(rand.NewSource(opt.Seed)),
		respawnDelay: opt.RespawnDelay,
		rooms:        make(map[string]*roomState, len(w.Rooms)),
		npcs:         make(map[string]*npcState, len(w.NPCs)),
		players:      make(map[string]*Player),
		groups:       make(map[string]*Group),
	}
	for id, r := range w.Rooms {
		g.rooms[id] = &roomState{
			id:    id,
			def:   r,
			items: append([]string(nil), r.Items...),
			npcs:  append([]string(nil), r.NPCs...),
		}
	}
	for id, n := range w.NPCs {
		st := &npcState{id: id, def: n, alive: true}
		if n.Stats != nil {
			st.hp = n.Stats.HP
		}
		g.npcs[id] = st
	}
	return g
}

// ---------------------------------------------------------------------------
// Outgoing messages

type message struct {
	to   *Player
	line string
}

// outbox collects events while a command runs. Recipients are resolved when
// the event is added, so "old room" / "new room" semantics are exact.
type outbox struct {
	msgs []message
}

func (o *outbox) to(p *Player, line string) {
	o.msgs = append(o.msgs, message{to: p, line: line})
}

func (g *Game) toRoom(o *outbox, roomID string, except *Player, line string) {
	if r, ok := g.rooms[roomID]; ok {
		for _, p := range r.players {
			if p != except {
				o.to(p, line)
			}
		}
	}
}

func (g *Game) toAll(o *outbox, line string) {
	for _, p := range g.players {
		o.to(p, line)
	}
}

// flush delivers queued events. Must be called with g.mu held so that every
// client observes events in the same order.
func (g *Game) flush(o *outbox) {
	for _, m := range o.msgs {
		m.to.sink.Send(m.line)
	}
	if len(o.msgs) > 0 {
		g.log.Debug("events delivered", "count", len(o.msgs))
	}
	o.msgs = nil
}

func (g *Game) reply(p *Player, line string, cmd string) {
	p.sink.Send(line)
	LogReply(g.log, p.Name, p.Addr, cmd, line)
}

// LogReply logs a reply sent to a client: errors at WARN, successes at INFO.
func LogReply(log *slog.Logger, name, addr, cmd, line string) {
	if strings.HasPrefix(line, "ERR ") {
		f := strings.Fields(line)
		code := ""
		if len(f) > 1 {
			code = f[1]
		}
		log.Warn("error response", "type", "response", "player", name, "ip", addr, "command", cmd, "code", code, "response", line)
		return
	}
	log.Info("response", "type", "response", "player", name, "ip", addr, "command", cmd, "response", truncate(line, 256))
}

func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n] + "..."
}

func mustJSON(v any) string {
	b, err := json.Marshal(v)
	if err != nil {
		return "{}"
	}
	return string(b)
}

// ---------------------------------------------------------------------------
// Connection lifecycle

// ValidName reports whether name is an acceptable username: 1..24 Unicode
// letters, digits, '_' or '-'.
func ValidName(name string) bool {
	if name == "" || utf8.RuneCountInString(name) > MaxNameLen {
		return false
	}
	for _, r := range name {
		if !unicode.IsLetter(r) && !unicode.IsDigit(r) && r != '_' && r != '-' {
			return false
		}
	}
	return true
}

// Connect authenticates a client. It returns the player (nil on failure)
// after having sent the reply line through sink.
func (g *Game) Connect(args string, sink Sink, addr string) *Player {
	g.mu.Lock()
	defer g.mu.Unlock()

	name := strings.TrimSpace(args)
	send := func(line string) {
		sink.Send(line)
		LogReply(g.log, name, addr, "CONNECT", line)
	}
	if name == "" || strings.ContainsAny(name, " \t") {
		send(errInvalidArgs)
		return nil
	}
	if !ValidName(name) {
		send(errInvalidName)
		return nil
	}
	key := strings.ToLower(name)
	if _, taken := g.players[key]; taken {
		send(errNameInUse)
		return nil
	}

	p := &Player{
		Name:    name,
		Addr:    addr,
		sink:    sink,
		room:    g.w.SpawnRoom,
		hp:      PlayerMaxHP,
		maxHP:   PlayerMaxHP,
		invites: make(map[string]bool),
		quests:  make(map[string]*questState),
		talks:   make(map[string]int),
	}
	g.players[key] = p
	g.rooms[p.room].players = append(g.rooms[p.room].players, p)
	send("OK connected")
	g.log.Info("player joined", "type", "session", "player", name, "ip", addr, "room", p.room, "online", len(g.players))

	var o outbox
	g.toRoom(&o, p.room, p, "EVT ROOM PRESENCE ENTER "+p.Name)
	g.toAll(&o, statsLine(len(g.players)))
	g.flush(&o)
	return p
}

// Disconnect removes every trace of the player from the world first, then
// broadcasts the leave events. Held items are dropped where the player stood
// so that unique items are never lost.
func (g *Game) Disconnect(p *Player, reason string) {
	g.mu.Lock()
	defer g.mu.Unlock()
	if g.players[strings.ToLower(p.Name)] != p {
		return
	}

	delete(g.players, strings.ToLower(p.Name))
	room := g.rooms[p.room]
	room.players = removePlayer(room.players, p)
	dropped := p.inventory
	room.items = append(room.items, dropped...)
	p.inventory = nil
	target := p.target
	p.target = ""
	var groupMembers []*Player
	if grp := p.group; grp != nil {
		g.removeFromGroup(p)
		groupMembers = grp.Members
	}

	g.log.Info("player left", "type", "session", "player", p.Name, "ip", p.Addr, "reason", reason,
		"room", room.id, "dropped_items", dropped, "online", len(g.players))
	if target != "" {
		g.resetIfDisengaged(target)
	}

	var o outbox
	g.toRoom(&o, room.id, nil, "EVT ROOM PRESENCE LEAVE "+p.Name)
	for _, it := range dropped {
		g.toRoom(&o, room.id, nil, "EVT ROOM ITEM DROPPED "+p.Name+" "+it)
	}
	for _, m := range groupMembers {
		o.to(m, "EVT GROUP LEAVE "+p.Name)
	}
	g.toAll(&o, statsLine(len(g.players)))
	g.flush(&o)
}

func statsLine(n int) string {
	return "EVT STATS players=" + strconv.Itoa(n)
}

func removePlayer(list []*Player, p *Player) []*Player {
	for i, q := range list {
		if q == p {
			return append(list[:i:i], list[i+1:]...)
		}
	}
	return list
}

// ---------------------------------------------------------------------------
// Command dispatch

type handler func(g *Game, p *Player, args string, o *outbox) string

var handlers map[string]handler

func init() {
	handlers = map[string]handler{
		"LOOK":      (*Game).cmdLook,
		"MOVE":      (*Game).cmdMove,
		"CHAT":      (*Game).cmdChat,
		"WHO":       (*Game).cmdWho,
		"GROUP":     (*Game).cmdGroup,
		"TAKE":      (*Game).cmdTake,
		"DROP":      (*Game).cmdDrop,
		"INVENTORY": (*Game).cmdInventory,
		"TALK":      (*Game).cmdTalk,
		"ATTACK":    (*Game).cmdAttack,
		"DEFEND":    (*Game).cmdDefend,
		"FLEE":      (*Game).cmdFlee,
		"USE":       (*Game).cmdUse,
		"STATUS":    (*Game).cmdStatus,
		"QUEST":     (*Game).cmdQuest,
		"QUESTS":    (*Game).cmdQuests,
	}
}

// SplitCommand splits a protocol line into an upper-cased command name and
// its (trimmed) argument string.
func SplitCommand(line string) (string, string) {
	line = strings.TrimSpace(line)
	cmd, args, _ := strings.Cut(line, " ")
	return strings.ToUpper(cmd), strings.TrimSpace(args)
}

// Handle runs one command for an authenticated player. QUIT and CONNECT are
// handled by the network layer.
func (g *Game) Handle(p *Player, cmd, args string) {
	g.mu.Lock()
	defer g.mu.Unlock()
	if g.players[strings.ToLower(p.Name)] != p {
		return
	}
	h, ok := handlers[cmd]
	if !ok {
		g.reply(p, errUnknownCommand, cmd)
		return
	}
	var o outbox
	line := h(g, p, args, &o)
	g.reply(p, line, cmd)
	g.flush(&o)
}

// Online returns the number of authenticated players.
func (g *Game) Online() int {
	g.mu.Lock()
	defer g.mu.Unlock()
	return len(g.players)
}
