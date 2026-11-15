package sharedspace

import (
	"context"
	"fmt"
	"strings"
	"sync"
	"time"
)

// memStore is an in-memory Store for tests that do not need PostgreSQL. The
// SQL implementation is covered by internal/integration.
type memStore struct {
	mu       sync.Mutex
	seq      int
	roles    map[string]string   // user id -> role
	emails   map[string]string   // user id -> email
	groups   map[string]*Group   // group id -> group
	grants   map[string]Grant    // grant id -> grant
	links    map[string]*linkRow // link id -> link
	settings map[string]string
	notices  []string // titles of admin notifications
}

func newMemStore() *memStore {
	return &memStore{
		roles:    map[string]string{},
		emails:   map[string]string{},
		groups:   map[string]*Group{},
		grants:   map[string]Grant{},
		links:    map[string]*linkRow{},
		settings: map[string]string{},
	}
}

func (m *memStore) nextID(prefix string) string {
	m.seq++
	return fmt.Sprintf("%s-%d", prefix, m.seq)
}

func (m *memStore) addUser(id, role string) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.roles[id] = role
	m.emails[id] = id + "@example.test"
}

func (m *memStore) removeUser(id string) {
	m.mu.Lock()
	defer m.mu.Unlock()
	delete(m.roles, id)
}

func (m *memStore) UserGrants(_ context.Context, userID string) ([]UserGrant, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	var out []UserGrant
	for _, g := range m.grants {
		applies := false
		switch g.SubjectType {
		case SubjectEveryone:
			applies = true
		case SubjectUser:
			applies = g.UserID == userID
		case SubjectGroup:
			if group, ok := m.groups[g.GroupID]; ok {
				for _, member := range group.Members {
					if member.UserID == userID {
						applies = true
					}
				}
			}
		}
		if !applies {
			continue
		}
		level, _ := parseGrantPermission(g.Permission)
		out = append(out, UserGrant{Path: g.Path, Level: level})
	}
	return out, nil
}

func (m *memStore) UserRole(_ context.Context, userID string) (string, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	role, ok := m.roles[userID]
	if !ok {
		return "", ErrUserNotFound
	}
	return role, nil
}

func (m *memStore) ListGrants(context.Context) ([]Grant, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	out := []Grant{}
	for _, g := range m.grants {
		out = append(out, g)
	}
	return out, nil
}

func (m *memStore) PutGrant(_ context.Context, grant Grant, _ string) (Grant, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	switch grant.SubjectType {
	case SubjectUser:
		if _, ok := m.roles[grant.UserID]; !ok {
			return Grant{}, ErrUserNotFound
		}
	case SubjectGroup:
		if _, ok := m.groups[grant.GroupID]; !ok {
			return Grant{}, ErrGroupNotFound
		}
	}
	for id, existing := range m.grants {
		if existing.Path == grant.Path && existing.SubjectType == grant.SubjectType &&
			existing.UserID == grant.UserID && existing.GroupID == grant.GroupID {
			existing.Permission = grant.Permission
			m.grants[id] = existing
			return existing, nil
		}
	}
	grant.ID = m.nextID("grant")
	grant.CreatedAt = time.Now()
	m.grants[grant.ID] = grant
	return grant, nil
}

func (m *memStore) DeleteGrant(_ context.Context, id string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if _, ok := m.grants[id]; !ok {
		return ErrGrantNotFound
	}
	delete(m.grants, id)
	return nil
}

func (m *memStore) ListGroups(context.Context) ([]Group, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	out := []Group{}
	for _, g := range m.groups {
		out = append(out, *g)
	}
	return out, nil
}

func (m *memStore) CreateGroup(_ context.Context, name string) (Group, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	for _, g := range m.groups {
		if strings.EqualFold(g.Name, name) {
			return Group{}, ErrGroupExists
		}
	}
	group := &Group{ID: m.nextID("group"), Name: name, CreatedAt: time.Now(), Members: []GroupMember{}}
	m.groups[group.ID] = group
	return *group, nil
}

func (m *memStore) DeleteGroup(_ context.Context, id string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if _, ok := m.groups[id]; !ok {
		return ErrGroupNotFound
	}
	delete(m.groups, id)
	for grantID, g := range m.grants {
		if g.GroupID == id {
			delete(m.grants, grantID)
		}
	}
	return nil
}

func (m *memStore) AddGroupMember(_ context.Context, groupID, userID string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	group, ok := m.groups[groupID]
	if !ok {
		return ErrGroupNotFound
	}
	if _, ok := m.roles[userID]; !ok {
		return ErrUserNotFound
	}
	for _, member := range group.Members {
		if member.UserID == userID {
			return nil
		}
	}
	group.Members = append(group.Members, GroupMember{UserID: userID, Email: m.emails[userID]})
	return nil
}

func (m *memStore) RemoveGroupMember(_ context.Context, groupID, userID string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	group, ok := m.groups[groupID]
	if !ok {
		return nil
	}
	kept := group.Members[:0]
	for _, member := range group.Members {
		if member.UserID != userID {
			kept = append(kept, member)
		}
	}
	group.Members = kept
	return nil
}

func (m *memStore) RenamePath(_ context.Context, oldRel, newRel string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	move := func(path string) (string, bool) {
		if path == oldRel {
			return newRel, true
		}
		if strings.HasPrefix(path, oldRel+"/") {
			return newRel + strings.TrimPrefix(path, oldRel), true
		}
		return path, false
	}
	for id, g := range m.grants {
		if moved, ok := move(g.Path); ok {
			g.Path = moved
			m.grants[id] = g
		}
	}
	for _, link := range m.links {
		if moved, ok := move(link.Path); ok {
			link.Path = moved
		}
	}
	return nil
}

func (m *memStore) DeletePath(_ context.Context, rel string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	now := time.Now()
	for id, g := range m.grants {
		if isUnder(g.Path, rel) {
			delete(m.grants, id)
		}
	}
	for _, link := range m.links {
		if isUnder(link.Path, rel) && link.RevokedAt == nil {
			link.RevokedAt = &now
		}
	}
	return nil
}

func (m *memStore) CreateLink(_ context.Context, link linkRow) (linkRow, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	link.ID = m.nextID("link")
	link.CreatorEmail = m.emails[link.CreatedBy]
	stored := link
	m.links[link.ID] = &stored
	return link, nil
}

func (m *memStore) LinkByTokenHash(_ context.Context, tokenHash string) (linkRow, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	for _, link := range m.links {
		if link.TokenHash == tokenHash {
			return *link, nil
		}
	}
	return linkRow{}, ErrLinkNotFound
}

func (m *memStore) ListLinks(_ context.Context, userID string, all bool) ([]linkRow, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	out := []linkRow{}
	for _, link := range m.links {
		if link.RevokedAt == nil && (all || link.CreatedBy == userID) {
			out = append(out, *link)
		}
	}
	return out, nil
}

func (m *memStore) RevokeLink(_ context.Context, id, userID string, anyOwner bool) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	link, ok := m.links[id]
	if !ok || link.RevokedAt != nil || (!anyOwner && link.CreatedBy != userID) {
		return ErrLinkNotFound
	}
	now := time.Now()
	link.RevokedAt = &now
	return nil
}

func (m *memStore) CountDownload(_ context.Context, id string) (bool, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	link, ok := m.links[id]
	if !ok || link.RevokedAt != nil {
		return false, nil
	}
	if link.MaxDownloads != nil && link.DownloadCount >= *link.MaxDownloads {
		return false, nil
	}
	link.DownloadCount++
	return true, nil
}

func (m *memStore) Setting(_ context.Context, key string) (string, bool, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	value, ok := m.settings[key]
	return value, ok, nil
}

func (m *memStore) SetSetting(_ context.Context, key, value string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.settings[key] = value
	return nil
}

func (m *memStore) NotifyAdmins(_ context.Context, _ string, title, _ string) ([]string, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.notices = append(m.notices, title)
	return nil, nil
}

func (m *memStore) noticeTitles() []string {
	m.mu.Lock()
	defer m.mu.Unlock()
	return append([]string(nil), m.notices...)
}

// rawLinks exposes stored rows so tests can check what is persisted.
func (m *memStore) rawLinks() []linkRow {
	m.mu.Lock()
	defer m.mu.Unlock()
	out := []linkRow{}
	for _, link := range m.links {
		out = append(out, *link)
	}
	return out
}
