package game

import (
	"encoding/json"
	"io"
	"log/slog"
	"strings"
	"sync"
	"testing"
	"time"

	"tap/internal/world"
)

type sink struct {
	mu    sync.Mutex
	lines []string
}

func (s *sink) Send(line string) {
	s.mu.Lock()
	s.lines = append(s.lines, line)
	s.mu.Unlock()
}

// take returns and clears everything received so far.
func (s *sink) take() []string {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := s.lines
	s.lines = nil
	return out
}

func newGame(t *testing.T) *Game {
	t.Helper()
	w, warns, err := world.Load("../../world/world.json")
	if err != nil {
		t.Fatal(err)
	}
	if len(warns) > 0 {
		t.Fatalf("world warnings: %v", warns)
	}
	log := slog.New(slog.NewJSONHandler(io.Discard, nil))
	return New(w, log, Options{Seed: 42, RespawnDelay: time.Hour})
}

func join(t *testing.T, g *Game, name string) (*Player, *sink) {
	t.Helper()
	s := &sink{}
	p := g.Connect(name, s, "127.0.0.1")
	if p == nil {
		t.Fatalf("connect %s: %v", name, s.take())
	}
	s.take()
	return p, s
}

// do runs a command and returns the reply (first line) and the events.
func do(g *Game, p *Player, s *sink, line string) (string, []string) {
	cmd, args := SplitCommand(line)
	g.Handle(p, cmd, args)
	out := s.take()
	if len(out) == 0 {
		return "", nil
	}
	return out[0], out[1:]
}

func teleport(g *Game, p *Player, room string) {
	g.mu.Lock()
	defer g.mu.Unlock()
	g.rooms[p.room].players = removePlayer(g.rooms[p.room].players, p)
	p.room = room
	g.rooms[room].players = append(g.rooms[room].players, p)
}

func expect(t *testing.T, got, want string) {
	t.Helper()
	if got != want {
		t.Fatalf("got %q, want %q", got, want)
	}
}

func expectPrefix(t *testing.T, got, prefix string) {
	t.Helper()
	if !strings.HasPrefix(got, prefix) {
		t.Fatalf("got %q, want prefix %q", got, prefix)
	}
}

func TestWorldMeetsRequirements(t *testing.T) {
	g := newGame(t)
	if n := len(g.w.Rooms); n < 8 {
		t.Fatalf("only %d rooms", n)
	}
	roles := map[string]bool{}
	for _, n := range g.w.NPCs {
		roles[n.Role] = true
	}
	if len(roles) < 3 {
		t.Fatalf("only %d npc roles", len(roles))
	}
	if len(g.w.Items) < 4 || len(g.w.Quests) < 2 {
		t.Fatal("not enough items or quests")
	}
}

func TestValidationCatchesBrokenReferences(t *testing.T) {
	w := &world.World{
		SpawnRoom: "a", SafeRoom: "a",
		Rooms: map[string]*world.Room{
			"a": {Name: "A", Exits: map[string]string{"north": "missing"}, Items: []string{"item.ghost"}},
		},
	}
	errs, _ := w.Validate()
	if len(errs) != 2 {
		t.Fatalf("expected 2 errors, got %v", errs)
	}
}

func TestConnectNames(t *testing.T) {
	g := newGame(t)
	join(t, g, "alice")
	s := &sink{}
	if g.Connect("ALICE", s, "x") != nil {
		t.Fatal("duplicate name accepted")
	}
	expect(t, s.take()[0], "ERR 201 NAME_IN_USE")
	if g.Connect("bad\x1bname", s, "x") != nil {
		t.Fatal("control characters accepted")
	}
	expect(t, s.take()[0], "ERR 202 INVALID_NAME")
	if g.Connect("Zoë", s, "x") == nil {
		t.Fatal("unicode name refused")
	}
}

func TestLookMoveAndPresence(t *testing.T) {
	g := newGame(t)
	a, as := join(t, g, "alice")
	b, bs := join(t, g, "bob")
	as.take()

	reply, _ := do(g, a, as, "LOOK")
	var look lookReply
	if err := json.Unmarshal([]byte(strings.TrimPrefix(reply, "OK ")), &look); err != nil {
		t.Fatal(err)
	}
	if look.Room.ID != "loc.novaris_gate" || len(look.Players) != 2 {
		t.Fatalf("unexpected look: %+v", look)
	}

	reply, _ = do(g, a, as, "MOVE north")
	expect(t, reply, "OK room=loc.watch_post")
	expect(t, bs.take()[0], "EVT ROOM PRESENCE LEAVE alice")
	reply, _ = do(g, b, bs, "move N")
	expect(t, reply, "OK room=loc.watch_post")
	expect(t, as.take()[0], "EVT ROOM PRESENCE ENTER bob")
	reply, _ = do(g, a, as, "MOVE nowhere")
	expect(t, reply, "ERR 301 NO_EXIT")
	reply, _ = do(g, a, as, "WHO")
	expect(t, reply, "OK players=2")
}

func TestItemsAreUnique(t *testing.T) {
	g := newGame(t)
	a, as := join(t, g, "alice")
	b, bs := join(t, g, "bob")
	as.take()
	teleport(g, a, "loc.cemetery")
	teleport(g, b, "loc.cemetery")

	reply, _ := do(g, a, as, "TAKE healing HERBS")
	expect(t, reply, "OK taken=item.healing_herbs")
	expect(t, bs.take()[0], "EVT ROOM ITEM TAKEN alice item.healing_herbs")
	reply, _ = do(g, b, bs, "TAKE item.healing_herbs")
	expect(t, reply, "ERR 404 ITEM_NOT_FOUND")
	reply, _ = do(g, b, bs, "DROP Healing Herbs")
	expect(t, reply, "ERR 404 ITEM_NOT_IN_INVENTORY")
	reply, _ = do(g, a, as, "DROP herbs")
	expect(t, reply, "OK dropped=item.healing_herbs")
	expect(t, bs.take()[0], "EVT ROOM ITEM DROPPED alice item.healing_herbs")
	reply, _ = do(g, b, bs, "TAKE healing_herbs")
	expect(t, reply, "OK taken=item.healing_herbs")
	reply, _ = do(g, b, bs, "INVENTORY")
	expect(t, reply, `OK ["item.healing_herbs"]`)

	expect(t, as.take()[0], "EVT ROOM ITEM TAKEN bob item.healing_herbs")
	teleport(g, a, "loc.ancient_vault")
	reply, _ = do(g, a, as, "TAKE Vault Seal")
	expect(t, reply, "ERR 408 ITEM_NOT_OBTAINABLE")
}

func TestChatScopes(t *testing.T) {
	g := newGame(t)
	a, as := join(t, g, "alice")
	b, bs := join(t, g, "bob")
	as.take()
	reply, ev := do(g, a, as, "CHAT GLOBAL Hello   everyone")
	expect(t, reply, "OK")
	expect(t, ev[0], "EVT GLOBAL CHAT alice Hello   everyone")
	expect(t, bs.take()[0], "EVT GLOBAL CHAT alice Hello   everyone")

	teleport(g, b, "loc.bakery")
	reply, _ = do(g, a, as, "CHAT ROOM psst")
	expect(t, reply, "OK")
	if got := bs.take(); len(got) != 0 {
		t.Fatalf("room chat leaked: %v", got)
	}
	reply, _ = do(g, a, as, "CHAT GROUP hi")
	expect(t, reply, "ERR 401 NOT_IN_GROUP")
	reply, _ = do(g, a, as, "CHAT SHOUT hi")
	expect(t, reply, "ERR 101 INVALID_ARGUMENTS")
	_ = b
}

func TestGroups(t *testing.T) {
	g := newGame(t)
	a, as := join(t, g, "alice")
	b, bs := join(t, g, "bob")
	c, cs := join(t, g, "carol")
	as.take()
	bs.take()

	reply, _ := do(g, b, bs, "GROUP JOIN alice")
	expect(t, reply, "ERR 401 NOT_IN_GROUP")
	reply, _ = do(g, a, as, "GROUP CREATE")
	expect(t, reply, "OK group=grp.1")
	reply, _ = do(g, a, as, "GROUP CREATE")
	expect(t, reply, "ERR 402 ALREADY_IN_GROUP")
	reply, _ = do(g, b, bs, "GROUP JOIN alice")
	expect(t, reply, "ERR 407 NOT_INVITED")
	reply, _ = do(g, a, as, "GROUP INVITE nobody")
	expect(t, reply, "ERR 403 PLAYER_NOT_FOUND")
	reply, _ = do(g, a, as, "GROUP INVITE bob")
	expect(t, reply, "OK")
	expect(t, bs.take()[0], "EVT GROUP INVITE alice")
	reply, ev := do(g, b, bs, "GROUP JOIN alice")
	expect(t, reply, "OK group=grp.1")
	expect(t, ev[0], "EVT GROUP JOIN bob")
	expect(t, as.take()[0], "EVT GROUP JOIN bob")

	reply, _ = do(g, b, bs, "CHAT GROUP hello team")
	expect(t, reply, "OK")
	expect(t, as.take()[0], "EVT GROUP CHAT bob hello team")
	if got := cs.take(); len(got) != 0 {
		t.Fatalf("group chat leaked: %v", got)
	}

	reply, _ = do(g, a, as, "GROUP LEAVE")
	expect(t, reply, "OK")
	expect(t, bs.take()[0], "EVT GROUP LEAVE alice")
	if g.groups["grp.1"].Leader != b {
		t.Fatal("leadership not transferred")
	}
	reply, _ = do(g, b, bs, "GROUP LEAVE")
	expect(t, reply, "OK")
	if len(g.groups) != 0 {
		t.Fatal("empty group not dissolved")
	}
	_ = c
}

func TestTalkAndHostility(t *testing.T) {
	g := newGame(t)
	a, as := join(t, g, "alice")
	teleport(g, a, "loc.guard_barracks")
	reply, _ := do(g, a, as, "TALK captain")
	expect(t, reply, "OK Keep the city safe. Trouble often begins where nobody is watching.")
	reply, _ = do(g, a, as, "ATTACK Captain Elian")
	expect(t, reply, "ERR 405 NPC_NOT_HOSTILE")
	reply, _ = do(g, a, as, "TALK goblin")
	expect(t, reply, "ERR 404 NPC_NOT_FOUND")
	reply, _ = do(g, a, as, "DEFEND")
	expect(t, reply, "ERR 410 NOT_IN_COMBAT")
}

func TestCombatVictoryLootAndQuestCredit(t *testing.T) {
	g := newGame(t)
	a, as := join(t, g, "alice")
	b, bs := join(t, g, "bob")
	as.take()
	teleport(g, a, "loc.docks")
	reply, _ := do(g, a, as, "QUEST dockmaster")
	expectPrefix(t, reply, `OK {"quest_id":"quest.smugglers_manifest"`)

	teleport(g, a, "loc.smugglers_tunnel")
	teleport(g, b, "loc.smugglers_tunnel")
	a.inventory = append(a.inventory, "item.guard_sword", "item.iron_shield")
	var res combatResult
	for i := 0; i < 30; i++ {
		a.hp = PlayerMaxHP // keep the test independent of dice
		reply, _ = do(g, a, as, "ATTACK vex")
		expectPrefix(t, reply, "OK {")
		if err := json.Unmarshal([]byte(reply[3:]), &res); err != nil {
			t.Fatal(err)
		}
		if res.Status == "victory" {
			break
		}
		expect(t, res.Status, "combat")
	}
	if res.Status != "victory" || res.TargetHP != 0 {
		t.Fatalf("vex not defeated: %+v", res)
	}
	if len(res.Loot) != 1 || res.Loot[0] != "item.smugglers_manifest" {
		t.Fatalf("unexpected loot %v", res.Loot)
	}
	seen := strings.Join(bs.take(), "\n")
	if !strings.Contains(seen, "EVT ROOM COMBAT") || !strings.Contains(seen, "EVT ROOM NPC DEFEATED npc.smuggler_vex alice") {
		t.Fatalf("bystander missed combat events:\n%s", seen)
	}
	reply, _ = do(g, a, as, "ATTACK vex")
	expect(t, reply, "ERR 404 NPC_NOT_FOUND")

	reply, _ = do(g, a, as, "TAKE manifest")
	expect(t, reply, "OK taken=item.smugglers_manifest")
	teleport(g, a, "loc.docks")
	reply, ev := do(g, a, as, "TALK Dockmaster Ren")
	expectPrefix(t, reply, "OK ")
	expectPrefix(t, strings.Join(ev, "\n"), "EVT QUEST COMPLETE")
	reply, _ = do(g, a, as, "QUESTS")
	expect(t, reply, `OK [{"quest_id":"quest.smugglers_manifest","name":"Smugglers Manifest","status":"completed","progress":"3/3"}]`)
	for _, it := range a.inventory {
		if it == "item.smugglers_manifest" {
			t.Fatal("delivered item not consumed")
		}
	}
	reply, _ = do(g, a, as, "QUEST ren")
	expect(t, reply, "ERR 406 NO_QUEST_AVAILABLE")
	_ = b
}

func TestDeathRespawnsAtSafeRoom(t *testing.T) {
	g := newGame(t)
	a, as := join(t, g, "alice")
	teleport(g, a, "loc.ancient_vault")
	var res combatResult
	for i := 0; i < 100 && res.Status != "defeated"; i++ {
		reply, _ := do(g, a, as, "ATTACK sentinel")
		if err := json.Unmarshal([]byte(reply[3:]), &res); err != nil {
			t.Fatal(reply)
		}
		if res.Status == "victory" {
			t.Fatal("unarmed player should not beat the sentinel")
		}
	}
	if res.Status != "defeated" || a.room != g.w.SafeRoom || a.hp != RespawnHP || a.target != "" {
		t.Fatalf("bad respawn: %+v room=%s hp=%d", res, a.room, a.hp)
	}
	if g.npcs["npc.vault_sentinel"].hp != g.w.NPCs["npc.vault_sentinel"].Stats.MaxHP {
		t.Fatal("enemy did not reset after the fight ended")
	}
	reply, _ := do(g, a, as, "STATUS")
	expect(t, reply, `OK {"hp":50,"max_hp":100,"status":"wounded","in_combat":false,"room":"loc.temple_hall"}`)
}

func TestQuestDeliveryAndPrerequisites(t *testing.T) {
	g := newGame(t)
	a, as := join(t, g, "alice")

	teleport(g, a, "loc.lighthouse")
	reply, _ := do(g, a, as, "QUEST ira")
	expect(t, reply, "ERR 406 NO_QUEST_AVAILABLE")

	teleport(g, a, "loc.healer_room")
	reply, _ = do(g, a, as, "TALK aria") // talking before accepting does nothing
	expectPrefix(t, reply, "OK ")
	reply, _ = do(g, a, as, "QUEST aria")
	expectPrefix(t, reply, `OK {"quest_id":"quest.herbal_remedy"`)
	if !strings.Contains(reply, `"status":"active","progress":"0/2"`) {
		t.Fatal(reply)
	}
	reply, _ = do(g, a, as, "TALK aria") // no herbs yet: delivery not done
	_, ev := do(g, a, as, "QUESTS")
	if len(ev) != 0 || !strings.Contains(reply, "OK ") {
		t.Fatal("unexpected events")
	}

	teleport(g, a, "loc.cemetery")
	_, ev = do(g, a, as, "TAKE herbs")
	expect(t, ev[0], `EVT QUEST PROGRESS {"quest_id":"quest.herbal_remedy","name":"Herbal Remedy","status":"active","progress":"1/2"}`)
	teleport(g, a, "loc.healer_room")
	reply, _ = do(g, a, as, "QUEST Healer Aria")
	if !strings.Contains(reply, `"status":"completed"`) {
		t.Fatal(reply)
	}
	reply, _ = do(g, a, as, "INVENTORY")
	expect(t, reply, `OK ["item.novaris_coins","item.healers_potion"]`)
	a.hp = 30
	reply, _ = do(g, a, as, "USE potion")
	expectPrefix(t, reply, `OK {"attacker_hp":70`)
}

func TestDisconnectCleansUpBeforeBroadcast(t *testing.T) {
	g := newGame(t)
	a, as := join(t, g, "alice")
	b, bs := join(t, g, "bob")
	as.take()
	do(g, a, as, "TAKE coins")
	do(g, a, as, "GROUP CREATE")
	do(g, a, as, "GROUP INVITE bob")
	do(g, b, bs, "GROUP JOIN alice")
	bs.take()

	as.take()
	g.Disconnect(a, "test")
	if got := as.take(); len(got) != 0 {
		t.Fatalf("leaving player received %v", got)
	}
	got := bs.take()
	want := []string{
		"EVT ROOM PRESENCE LEAVE alice",
		"EVT ROOM ITEM DROPPED alice item.novaris_coins",
		"EVT GROUP LEAVE alice",
		"EVT STATS players=1",
	}
	if strings.Join(got, "|") != strings.Join(want, "|") {
		t.Fatalf("got %v", got)
	}
	reply, _ := do(g, b, bs, "LOOK")
	if strings.Contains(reply, "alice") || !strings.Contains(reply, "item.novaris_coins") {
		t.Fatal(reply)
	}
	g.Handle(a, "LOOK", "") // stale player: ignored
	if len(as.take()) != 0 {
		t.Fatal("disconnected player still served")
	}
}

func TestNPCRespawn(t *testing.T) {
	g := newGame(t)
	g.respawnDelay = 10 * time.Millisecond
	a, as := join(t, g, "alice")
	teleport(g, a, "loc.flooded_sewer")
	a.inventory = []string{"item.guard_sword"}
	for i := 0; i < 20; i++ {
		a.hp = PlayerMaxHP
		if reply, _ := do(g, a, as, "ATTACK rat"); strings.Contains(reply, "victory") {
			break
		}
	}
	time.Sleep(100 * time.Millisecond)
	found := false
	for _, l := range as.take() {
		found = found || l == "EVT ROOM NPC SPAWN npc.giant_sewer_rat"
	}
	reply, _ := do(g, a, as, "LOOK")
	if !found || !strings.Contains(reply, "npc.giant_sewer_rat") {
		t.Fatal("rat did not respawn")
	}
}
