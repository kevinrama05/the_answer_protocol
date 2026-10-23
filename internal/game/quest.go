package game

import (
	"strconv"
	"strings"

	"tap/internal/world"
)

type questState struct {
	status   string // "active" or "completed"
	defeated map[string]bool
	talked   map[int]bool // talk_to objectives already satisfied
	progress string       // last progress reported to the client
}

type questInfo struct {
	QuestID     string   `json:"quest_id"`
	Name        string   `json:"name"`
	Description string   `json:"description"`
	Giver       string   `json:"giver"`
	Reward      string   `json:"reward"`
	Rewards     []string `json:"rewards"`
	Objectives  []string `json:"objectives"`
	Status      string   `json:"status"`
	Progress    string   `json:"progress"`
}

type questSummary struct {
	QuestID  string `json:"quest_id"`
	Name     string `json:"name"`
	Status   string `json:"status"`
	Progress string `json:"progress"`
}

func (g *Game) objectiveText(o world.Objective) string {
	switch o.Type {
	case world.ObjObtainItem:
		return "Obtain " + g.itemName(o.Item)
	case world.ObjTalkTo:
		return "Talk to " + g.npcName(o.NPC)
	case world.ObjDefeatNPC:
		return "Defeat " + g.npcName(o.NPC)
	}
	return o.Type
}

// objectiveDone evaluates objective i. Item objectives are checked against
// the live inventory, so dropping the item undoes the progress.
func objectiveDone(p *Player, st *questState, i int, o world.Objective) bool {
	switch o.Type {
	case world.ObjObtainItem:
		for _, it := range p.inventory {
			if it == o.Item {
				return true
			}
		}
		return false
	case world.ObjDefeatNPC:
		return st.defeated[o.NPC]
	case world.ObjTalkTo:
		return st.talked[i]
	}
	return false
}

func progressOf(p *Player, st *questState, q *world.Quest) (int, int) {
	if st.status == "completed" {
		return len(q.Objectives), len(q.Objectives)
	}
	done := 0
	for i, o := range q.Objectives {
		if objectiveDone(p, st, i, o) {
			done++
		}
	}
	return done, len(q.Objectives)
}

func progressString(done, total int) string {
	return strconv.Itoa(done) + "/" + strconv.Itoa(total)
}

func (g *Game) info(p *Player, qid string) questInfo {
	q := g.w.Quests[qid]
	st := p.quests[qid]
	inf := questInfo{
		QuestID:     qid,
		Name:        q.Name,
		Description: q.Description,
		Giver:       q.Giver,
		Reward:      strings.Join(q.Rewards, ","),
		Rewards:     append([]string{}, q.Rewards...),
		Status:      "available",
		Progress:    progressString(0, len(q.Objectives)),
	}
	for _, o := range q.Objectives {
		inf.Objectives = append(inf.Objectives, g.objectiveText(o))
	}
	if st != nil {
		inf.Status = st.status
		inf.Progress = progressString(progressOf(p, st, q))
	}
	return inf
}

// updateQuests re-evaluates every active quest of p. talkedTo is the NPC the
// player just talked to (TALK or QUEST), or "". A talk_to objective is only
// satisfied when every objective before it is already met, which turns
// "obtain X, talk to Y" into a delivery.
func (g *Game) updateQuests(p *Player, talkedTo string, o *outbox) {
	for _, qid := range p.questLog {
		st := p.quests[qid]
		if st.status != "active" {
			continue
		}
		q := g.w.Quests[qid]
		if talkedTo != "" {
			for i, obj := range q.Objectives {
				if obj.Type != world.ObjTalkTo || obj.NPC != talkedTo || st.talked[i] {
					continue
				}
				prior := true
				for j := 0; j < i; j++ {
					if !objectiveDone(p, st, j, q.Objectives[j]) {
						prior = false
						break
					}
				}
				if prior {
					st.talked[i] = true
				}
			}
		}
		done, total := progressOf(p, st, q)
		if done == total {
			g.completeQuest(p, qid, o)
			continue
		}
		if prog := progressString(done, total); prog != st.progress {
			st.progress = prog
			g.log.Info("quest progress", "type", "quest", "player", p.Name, "quest", qid, "progress", prog)
			o.to(p, "EVT QUEST PROGRESS "+mustJSON(questSummary{QuestID: qid, Name: q.Name, Status: "active", Progress: prog}))
		}
	}
}

// completeQuest consumes delivered items and grants the rewards. Reward
// items are new instances created by the quest giver.
func (g *Game) completeQuest(p *Player, qid string, o *outbox) {
	q := g.w.Quests[qid]
	st := p.quests[qid]
	st.status = "completed"
	for _, obj := range q.Objectives {
		if obj.Type == world.ObjObtainItem {
			p.inventory = removeFirst(p.inventory, obj.Item)
		}
	}
	p.inventory = append(p.inventory, q.Rewards...)
	st.progress = progressString(len(q.Objectives), len(q.Objectives))
	g.log.Info("quest completed", "type", "quest", "player", p.Name, "quest", qid, "rewards", q.Rewards)
	o.to(p, "EVT QUEST COMPLETE "+mustJSON(g.info(p, qid)))
}

func (g *Game) creditDefeat(p *Player, npcID string, o *outbox) {
	changed := false
	for _, qid := range p.questLog {
		st := p.quests[qid]
		if st.status != "active" {
			continue
		}
		for _, obj := range g.w.Quests[qid].Objectives {
			if obj.Type == world.ObjDefeatNPC && obj.NPC == npcID && !st.defeated[npcID] {
				st.defeated[npcID] = true
				changed = true
			}
		}
	}
	if changed {
		g.updateQuests(p, "", o)
	}
}

func (g *Game) cmdQuest(p *Player, args string, o *outbox) string {
	if args == "" {
		return errInvalidArgs
	}
	npcID, ok := resolve(args, g.rooms[p.room].npcs, g.npcName)
	if !ok {
		return errNPCNotFound
	}
	qid := g.w.NPCs[npcID].Quest
	if qid == "" {
		return errNoQuest
	}
	q := g.w.Quests[qid]
	for _, req := range q.Requires {
		if st := p.quests[req]; st == nil || st.status != "completed" {
			return errNoQuest
		}
	}
	st := p.quests[qid]
	switch {
	case st == nil:
		st = &questState{status: "active", defeated: map[string]bool{}, talked: map[int]bool{}}
		p.quests[qid] = st
		p.questLog = append(p.questLog, qid)
		st.progress = progressString(progressOf(p, st, q))
		g.log.Info("quest accepted", "type", "quest", "player", p.Name, "quest", qid, "npc", npcID)
	case st.status == "active":
		// Asking the giver again counts as talking to them (turn-in).
		g.updateQuests(p, npcID, o)
	default:
		return errNoQuest
	}
	return "OK " + mustJSON(g.info(p, qid))
}

func (g *Game) cmdQuests(p *Player, args string, _ *outbox) string {
	if args != "" {
		return errInvalidArgs
	}
	list := make([]questSummary, 0, len(p.questLog))
	for _, qid := range p.questLog {
		q := g.w.Quests[qid]
		list = append(list, questSummary{
			QuestID:  qid,
			Name:     q.Name,
			Status:   p.quests[qid].status,
			Progress: progressString(progressOf(p, p.quests[qid], q)),
		})
	}
	return "OK " + mustJSON(list)
}
