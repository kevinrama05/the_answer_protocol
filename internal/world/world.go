// Package world loads and validates the static world definition (rooms,
// items, NPCs and quests) from a JSON file.
package world

import (
	"bytes"
	"encoding/json"
	"fmt"
	"os"
	"sort"
)

// NPC roles understood by the server.
const (
	RoleDialogue   = "dialogue"
	RoleQuestGiver = "quest_giver"
	RoleMerchant   = "merchant"
	RoleEnemy      = "enemy"
)

// Quest objective types understood by the server.
const (
	ObjObtainItem = "obtain_item"
	ObjTalkTo     = "talk_to"
	ObjDefeatNPC  = "defeat_npc"
)

// File is the top-level layout of the world file.
type File struct {
	World World `json:"world"`
}

// World is the immutable world definition.
type World struct {
	ID          string            `json:"id"`
	Name        string            `json:"name"`
	Description string            `json:"description"`
	SpawnRoom   string            `json:"spawn_room"`
	SafeRoom    string            `json:"safe_room"`
	Rooms       map[string]*Room  `json:"rooms"`
	Items       map[string]*Item  `json:"items"`
	NPCs        map[string]*NPC   `json:"npcs"`
	Quests      map[string]*Quest `json:"quests"`
}

// Room is a location of the world.
type Room struct {
	Name        string            `json:"name"`
	Description string            `json:"description"`
	Exits       map[string]string `json:"exits"`
	Items       []string          `json:"items"`
	NPCs        []string          `json:"npcs"`
}

// Item is an item definition. Instances in the world reference it by ID.
type Item struct {
	Name        string `json:"name"`
	Description string `json:"description"`
	Category    string `json:"category"`
	Obtainable  bool   `json:"obtainable"`
	Value       int    `json:"value,omitempty"`
	Damage      int    `json:"damage,omitempty"`
	Defense     int    `json:"defense,omitempty"`
	Heal        int    `json:"heal,omitempty"`
}

// NPC is a non-player character definition.
type NPC struct {
	Name        string   `json:"name"`
	Description string   `json:"description"`
	Role        string   `json:"role"`
	Room        string   `json:"room"`
	Dialogue    []string `json:"dialogue,omitempty"`
	Quest       string   `json:"quest,omitempty"`
	Stats       *Stats   `json:"stats,omitempty"`
	Drops       []string `json:"drops,omitempty"`
}

// Stats are the combat statistics of an enemy NPC.
type Stats struct {
	HP     int `json:"hp"`
	MaxHP  int `json:"max_hp"`
	Damage int `json:"damage"`
}

// Quest is a quest definition.
type Quest struct {
	Name        string      `json:"name"`
	Giver       string      `json:"giver"`
	Description string      `json:"description"`
	Objectives  []Objective `json:"objectives"`
	Rewards     []string    `json:"rewards"`
	Requires    []string    `json:"requires,omitempty"`
}

// Objective is a single step of a quest.
type Objective struct {
	Type string `json:"type"`
	Item string `json:"item,omitempty"`
	NPC  string `json:"npc,omitempty"`
}

// Load reads, decodes and validates a world file. Unknown JSON fields are
// rejected so that typos in the data are caught at startup.
func Load(path string) (*World, []string, error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return nil, nil, fmt.Errorf("read world file: %w", err)
	}
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.DisallowUnknownFields()
	var f File
	if err := dec.Decode(&f); err != nil {
		return nil, nil, fmt.Errorf("parse world file: %w", err)
	}
	w := &f.World
	errs, warns := w.Validate()
	if len(errs) > 0 {
		return nil, warns, &ValidationError{Problems: errs}
	}
	return w, warns, nil
}

// ValidationError lists every problem found in a world definition.
type ValidationError struct {
	Problems []string
}

func (e *ValidationError) Error() string {
	var b bytes.Buffer
	fmt.Fprintf(&b, "invalid world (%d problems):", len(e.Problems))
	for _, p := range e.Problems {
		b.WriteString("\n  - ")
		b.WriteString(p)
	}
	return b.String()
}

var opposite = map[string]string{
	"north": "south", "south": "north", "east": "west", "west": "east",
	"up": "down", "down": "up",
	"northeast": "southwest", "southwest": "northeast",
	"northwest": "southeast", "southeast": "northwest",
}

// IsDirection reports whether d is a direction the server understands.
func IsDirection(d string) bool {
	_, ok := opposite[d]
	return ok
}

func sortedKeys[V any](m map[string]V) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys
}

// Validate checks every reference of the world. Errors make the world
// unusable; warnings (one-way exits, unreachable rooms) are only reported.
func (w *World) Validate() (errs []string, warns []string) {
	bad := func(format string, a ...any) { errs = append(errs, fmt.Sprintf(format, a...)) }

	if len(w.Rooms) == 0 {
		bad("world has no rooms")
		return errs, warns
	}
	if _, ok := w.Rooms[w.SpawnRoom]; !ok {
		bad("spawn_room %q does not exist", w.SpawnRoom)
	}
	if _, ok := w.Rooms[w.SafeRoom]; !ok {
		bad("safe_room %q does not exist", w.SafeRoom)
	}

	for _, rid := range sortedKeys(w.Rooms) {
		r := w.Rooms[rid]
		if r == nil {
			bad("room %s is null", rid)
			continue
		}
		if r.Name == "" {
			bad("room %s has no name", rid)
		}
		for _, dir := range sortedKeys(r.Exits) {
			target := r.Exits[dir]
			if !IsDirection(dir) {
				bad("room %s: unknown exit direction %q", rid, dir)
			}
			t, ok := w.Rooms[target]
			if !ok {
				bad("room %s: exit %s leads to unknown room %q", rid, dir, target)
				continue
			}
			if back := t.Exits[opposite[dir]]; back != rid {
				warns = append(warns, fmt.Sprintf("one-way exit %s -%s-> %s", rid, dir, target))
			}
		}
		for _, it := range r.Items {
			if _, ok := w.Items[it]; !ok {
				bad("room %s: unknown item %q", rid, it)
			}
		}
		for _, nid := range r.NPCs {
			n, ok := w.NPCs[nid]
			if !ok {
				bad("room %s: unknown npc %q", rid, nid)
			} else if n.Room != rid {
				bad("room %s lists npc %s whose room is %q", rid, nid, n.Room)
			}
		}
	}

	for _, iid := range sortedKeys(w.Items) {
		if it := w.Items[iid]; it == nil || it.Name == "" {
			bad("item %s has no name", iid)
		}
	}

	for _, nid := range sortedKeys(w.NPCs) {
		n := w.NPCs[nid]
		if n == nil || n.Name == "" {
			bad("npc %s has no name", nid)
			continue
		}
		r, ok := w.Rooms[n.Room]
		if !ok {
			bad("npc %s: unknown room %q", nid, n.Room)
		} else if !contains(r.NPCs, nid) {
			bad("npc %s: room %s does not list it", nid, n.Room)
		}
		switch n.Role {
		case RoleEnemy:
			if n.Stats == nil || n.Stats.HP <= 0 || n.Stats.Damage < 0 {
				bad("enemy npc %s needs stats with hp > 0 and damage >= 0", nid)
			} else if n.Stats.MaxHP < n.Stats.HP {
				n.Stats.MaxHP = n.Stats.HP
			}
		case RoleDialogue, RoleQuestGiver, RoleMerchant:
			if len(n.Dialogue) == 0 {
				bad("npc %s (%s) has no dialogue", nid, n.Role)
			}
		default:
			bad("npc %s: unknown role %q", nid, n.Role)
		}
		if n.Quest != "" {
			q, ok := w.Quests[n.Quest]
			if !ok {
				bad("npc %s: unknown quest %q", nid, n.Quest)
			} else if q.Giver != nid {
				bad("npc %s offers quest %s whose giver is %q", nid, n.Quest, q.Giver)
			}
		}
		for _, d := range n.Drops {
			if _, ok := w.Items[d]; !ok {
				bad("npc %s: unknown drop item %q", nid, d)
			}
		}
	}

	for _, qid := range sortedKeys(w.Quests) {
		q := w.Quests[qid]
		if q == nil {
			bad("quest %s is null", qid)
			continue
		}
		if g, ok := w.NPCs[q.Giver]; !ok {
			bad("quest %s: unknown giver %q", qid, q.Giver)
		} else if g.Quest != qid {
			bad("quest %s: giver %s does not offer it", qid, q.Giver)
		}
		if len(q.Objectives) == 0 {
			bad("quest %s has no objectives", qid)
		}
		for i, o := range q.Objectives {
			switch o.Type {
			case ObjObtainItem:
				if _, ok := w.Items[o.Item]; !ok {
					bad("quest %s objective %d: unknown item %q", qid, i, o.Item)
				}
			case ObjTalkTo:
				if _, ok := w.NPCs[o.NPC]; !ok {
					bad("quest %s objective %d: unknown npc %q", qid, i, o.NPC)
				}
			case ObjDefeatNPC:
				if n, ok := w.NPCs[o.NPC]; !ok {
					bad("quest %s objective %d: unknown npc %q", qid, i, o.NPC)
				} else if n.Role != RoleEnemy {
					bad("quest %s objective %d: npc %s is not an enemy", qid, i, o.NPC)
				}
			default:
				bad("quest %s objective %d: unknown type %q", qid, i, o.Type)
			}
		}
		for _, it := range q.Rewards {
			if _, ok := w.Items[it]; !ok {
				bad("quest %s: unknown reward item %q", qid, it)
			}
		}
		for _, req := range q.Requires {
			if _, ok := w.Quests[req]; !ok || req == qid {
				bad("quest %s: invalid prerequisite %q", qid, req)
			}
		}
	}

	if _, ok := w.Rooms[w.SpawnRoom]; ok {
		seen := map[string]bool{w.SpawnRoom: true}
		stack := []string{w.SpawnRoom}
		for len(stack) > 0 {
			cur := stack[len(stack)-1]
			stack = stack[:len(stack)-1]
			for _, t := range w.Rooms[cur].Exits {
				if _, ok := w.Rooms[t]; ok && !seen[t] {
					seen[t] = true
					stack = append(stack, t)
				}
			}
		}
		for _, rid := range sortedKeys(w.Rooms) {
			if !seen[rid] {
				warns = append(warns, fmt.Sprintf("room %s is unreachable from spawn", rid))
			}
		}
	}
	return errs, warns
}

func contains(list []string, s string) bool {
	for _, v := range list {
		if v == s {
			return true
		}
	}
	return false
}
