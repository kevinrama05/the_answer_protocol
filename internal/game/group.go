package game

import (
	"strconv"
	"strings"
)

func (g *Game) cmdGroup(p *Player, args string, o *outbox) string {
	sub, rest, _ := strings.Cut(args, " ")
	rest = strings.TrimSpace(rest)
	switch strings.ToUpper(sub) {
	case "CREATE":
		if rest != "" {
			return errInvalidArgs
		}
		return g.groupCreate(p)
	case "INVITE":
		if rest == "" || strings.Contains(rest, " ") {
			return errInvalidArgs
		}
		return g.groupInvite(p, rest, o)
	case "JOIN":
		if rest == "" || strings.Contains(rest, " ") {
			return errInvalidArgs
		}
		return g.groupJoin(p, rest, o)
	case "LEAVE":
		if rest != "" {
			return errInvalidArgs
		}
		if p.group == nil {
			return errNotInGroup
		}
		grp := p.group
		g.removeFromGroup(p)
		for _, m := range grp.Members {
			o.to(m, "EVT GROUP LEAVE "+p.Name)
		}
		return "OK"
	}
	return errInvalidArgs
}

func (g *Game) groupCreate(p *Player) string {
	if p.group != nil {
		return errAlreadyInGroup
	}
	g.groupSeq++
	grp := &Group{ID: "grp." + strconv.Itoa(g.groupSeq), Leader: p, Members: []*Player{p}}
	g.groups[grp.ID] = grp
	p.group = grp
	g.log.Info("group created", "type", "group", "player", p.Name, "group", grp.ID)
	return "OK group=" + grp.ID
}

func (g *Game) groupInvite(p *Player, name string, o *outbox) string {
	if p.group == nil {
		return errNotInGroup
	}
	t := g.players[strings.ToLower(name)]
	if t == nil {
		return errPlayerNotFound
	}
	if t == p {
		return errInvalidArgs
	}
	if t.group != nil {
		return errAlreadyInGroup
	}
	t.invites[p.group.ID] = true
	g.log.Info("group invite", "type", "group", "player", p.Name, "invitee", t.Name, "group", p.group.ID)
	o.to(t, "EVT GROUP INVITE "+p.group.Leader.Name)
	return "OK"
}

// groupJoin accepts the leader name (as in the RFC) or the group ID.
func (g *Game) groupJoin(p *Player, ref string, o *outbox) string {
	if p.group != nil {
		return errAlreadyInGroup
	}
	grp := g.groups[ref]
	if grp == nil {
		leader := g.players[strings.ToLower(ref)]
		if leader == nil {
			return errPlayerNotFound
		}
		if leader.group == nil {
			return errNotInGroup
		}
		grp = leader.group
	}
	if !p.invites[grp.ID] {
		return errNotInvited
	}
	delete(p.invites, grp.ID)
	grp.Members = append(grp.Members, p)
	p.group = grp
	g.log.Info("group joined", "type", "group", "player", p.Name, "group", grp.ID)
	for _, m := range grp.Members {
		o.to(m, "EVT GROUP JOIN "+p.Name)
	}
	return "OK group=" + grp.ID
}

// removeFromGroup detaches p, promotes a new leader or dissolves the group.
func (g *Game) removeFromGroup(p *Player) {
	grp := p.group
	if grp == nil {
		return
	}
	grp.Members = removePlayer(grp.Members, p)
	p.group = nil
	switch {
	case len(grp.Members) == 0:
		delete(g.groups, grp.ID)
		g.log.Info("group dissolved", "type", "group", "group", grp.ID)
	case grp.Leader == p:
		grp.Leader = grp.Members[0]
		g.log.Info("group leader changed", "type", "group", "group", grp.ID, "leader", grp.Leader.Name)
	}
	g.log.Info("group left", "type", "group", "player", p.Name, "group", grp.ID)
}
