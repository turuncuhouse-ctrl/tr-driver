package sharedspace

import "strings"

// Level is an effective permission on a path.
type Level int

const (
	LevelNone Level = iota
	LevelRead
	LevelWrite
	// LevelAdmin is held by administrators only; it implies write everywhere
	// plus managing grants, groups and the quota.
	LevelAdmin
)

func (l Level) String() string {
	switch l {
	case LevelRead:
		return "read"
	case LevelWrite:
		return "write"
	case LevelAdmin:
		return "admin"
	default:
		return "none"
	}
}

func parseGrantPermission(value string) (Level, bool) {
	switch strings.ToLower(strings.TrimSpace(value)) {
	case "read":
		return LevelRead, true
	case "write":
		return LevelWrite, true
	default:
		return LevelNone, false
	}
}

// Subject types a grant can be issued to.
const (
	SubjectUser     = "user"
	SubjectGroup    = "group"
	SubjectEveryone = "everyone"
)

// UserGrant is a grant that applies to one specific user, already resolved
// through group membership and the "everyone" subject.
type UserGrant struct {
	Path  string
	Level Level
}

// effectiveLevel is the highest level granted on rel itself or on any of its
// ancestors. There are no deny entries, so the result never drops when moving
// down the tree.
func effectiveLevel(grants []UserGrant, rel string) Level {
	level := LevelNone
	for _, g := range grants {
		if g.Level > level && isUnder(rel, g.Path) {
			level = g.Level
		}
	}
	return level
}

// traverseChildren returns the names directly below rel that lead to a granted
// path. A user who only holds a grant on "Filmler/2024" may walk through
// "Filmler" to reach it, but sees nothing else on the way.
func traverseChildren(grants []UserGrant, rel string) map[string]bool {
	out := map[string]bool{}
	for _, g := range grants {
		if g.Level == LevelNone || g.Path == rel || !isUnder(g.Path, rel) {
			continue
		}
		rest := g.Path
		if rel != "" {
			rest = strings.TrimPrefix(g.Path, rel+"/")
		}
		if i := strings.IndexByte(rest, '/'); i >= 0 {
			rest = rest[:i]
		}
		if rest != "" {
			out[rest] = true
		}
	}
	return out
}

// highestLevel is the best level the user holds anywhere in the tree.
func highestLevel(grants []UserGrant) Level {
	level := LevelNone
	for _, g := range grants {
		if g.Level > level {
			level = g.Level
		}
	}
	return level
}
