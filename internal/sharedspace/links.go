package sharedspace

import (
	"context"
	"errors"
	"fmt"
	"os"
	"sort"
	"strings"
	"time"
)

// Link is a public, time-limited link to a file or folder of the shared space.
type Link struct {
	ID            string    `json:"id"`
	Path          string    `json:"path"`
	Name          string    `json:"name"`
	HasPassword   bool      `json:"hasPassword"`
	ExpiresAt     time.Time `json:"expiresAt"`
	MaxDownloads  *int64    `json:"maxDownloads"`
	DownloadCount int64     `json:"downloadCount"`
	CreatedAt     time.Time `json:"createdAt"`
	CreatedBy     string    `json:"createdBy,omitempty"`
	// URL and Token are only set in the response that creates the link. The
	// server keeps a hash, so they cannot be shown again later.
	URL   string `json:"url,omitempty"`
	Token string `json:"token,omitempty"`
}

func linkFromRow(row linkRow) Link {
	return Link{
		ID:            row.ID,
		Path:          row.Path,
		Name:          baseRel(row.Path),
		HasPassword:   row.PasswordHash != "",
		ExpiresAt:     row.ExpiresAt,
		MaxDownloads:  row.MaxDownloads,
		DownloadCount: row.DownloadCount,
		CreatedAt:     row.CreatedAt,
		CreatedBy:     row.CreatorEmail,
	}
}

// LinkInput describes a link to create.
type LinkInput struct {
	Path string `json:"path"`
	// Password is optional; empty means the link works without one.
	Password string `json:"password"`
	// ExpiresInHours is how long the link stays valid. Zero picks the default
	// (seven days, or the server maximum when that is shorter).
	ExpiresInHours float64 `json:"expiresInHours"`
	MaxDownloads   *int64  `json:"maxDownloads"`
}

// CreateLink publishes a path. Publishing hands the content to people outside
// the server, so it needs write access, not just read; and every link expires.
func (s *Service) CreateLink(ctx context.Context, actor Actor, in LinkInput) (Link, error) {
	rel, _, err := s.require(ctx, actor, in.Path, linkCreateLevel)
	if err != nil {
		return Link{}, err
	}
	if rel == "" {
		return Link{}, fmt.Errorf("%w: Ortak Paylaşım kökü bağlantıyla paylaşılamaz, bir klasör veya dosya seçin", ErrInvalidInput)
	}
	if _, err := s.tree.lstat(rel); err != nil {
		return Link{}, err
	}
	ttl := time.Duration(in.ExpiresInHours * float64(time.Hour))
	switch {
	case in.ExpiresInHours < 0:
		return Link{}, fmt.Errorf("%w: geçerlilik süresi negatif olamaz", ErrInvalidInput)
	case in.ExpiresInHours == 0:
		ttl = min(defaultLinkTTL, s.opts.LinkMaxTTL)
	case ttl < minLinkTTL:
		return Link{}, fmt.Errorf("%w: geçerlilik süresi en az bir dakika olmalı", ErrInvalidInput)
	case ttl > s.opts.LinkMaxTTL:
		return Link{}, fmt.Errorf("%w: geçerlilik süresi en fazla %d saat olabilir", ErrInvalidInput, int(s.opts.LinkMaxTTL.Hours()))
	}
	if in.MaxDownloads != nil && (*in.MaxDownloads < 1 || *in.MaxDownloads > maxLinkMaxDownload) {
		return Link{}, fmt.Errorf("%w: indirme sınırı 1 ile %d arasında olmalı", ErrInvalidInput, maxLinkMaxDownload)
	}
	passwordHash := ""
	if in.Password != "" {
		if len(in.Password) < minLinkPassword || len(in.Password) > maxLinkPassword {
			return Link{}, fmt.Errorf("%w: şifre %d-%d karakter olmalı", ErrInvalidInput, minLinkPassword, maxLinkPassword)
		}
		if passwordHash, err = hashLinkPassword(in.Password); err != nil {
			return Link{}, err
		}
	}
	token, tokenHash, err := newLinkToken()
	if err != nil {
		return Link{}, err
	}
	now := s.now().UTC()
	row, err := s.store.CreateLink(ctx, linkRow{
		TokenHash:    tokenHash,
		Path:         rel,
		PasswordHash: passwordHash,
		ExpiresAt:    now.Add(ttl),
		MaxDownloads: in.MaxDownloads,
		CreatedBy:    actor.ID,
		CreatedAt:    now,
	})
	if err != nil {
		return Link{}, err
	}
	link := linkFromRow(row)
	link.Token = token
	link.URL = PublicPrefix + token
	return link, nil
}

// ListLinks returns the caller's active links; administrators see everyone's.
func (s *Service) ListLinks(ctx context.Context, actor Actor) ([]Link, error) {
	if actor.ID == "" {
		return nil, ErrForbidden
	}
	rows, err := s.store.ListLinks(ctx, actor.ID, actor.IsAdmin())
	if err != nil {
		return nil, err
	}
	now := s.now()
	out := make([]Link, 0, len(rows))
	for _, row := range rows {
		if !row.ExpiresAt.After(now) {
			continue
		}
		link := linkFromRow(row)
		if !actor.IsAdmin() {
			link.CreatedBy = ""
		}
		out = append(out, link)
	}
	return out, nil
}

// RevokeLink disables a link. Users revoke their own; administrators any.
func (s *Service) RevokeLink(ctx context.Context, actor Actor, id string) error {
	if actor.ID == "" {
		return ErrForbidden
	}
	return s.store.RevokeLink(ctx, strings.TrimSpace(id), actor.ID, actor.IsAdmin())
}

// PublicLink is a resolved, currently valid link.
type PublicLink struct {
	row   linkRow
	Name  string
	IsDir bool
	// NeedsPassword is true when the link is password-protected.
	NeedsPassword bool
	ExpiresAt     time.Time
}

// ID identifies the link (used to name the unlock cookie).
func (l PublicLink) ID() string { return l.row.ID }

// ResolveLink looks a token up and checks everything that can invalidate a
// link: revocation, expiry, the download allowance, the target still existing,
// and the creator still being allowed to publish it. That last check means
// removing someone's access (or their account) also turns off their links.
func (s *Service) ResolveLink(ctx context.Context, token string) (PublicLink, error) {
	token = strings.TrimSpace(token)
	// Tokens are 43 base64url characters; anything else cannot match and is
	// not worth a database round trip.
	if len(token) < 32 || len(token) > 64 {
		return PublicLink{}, ErrLinkNotFound
	}
	row, err := s.store.LinkByTokenHash(ctx, hashLinkToken(token))
	if err != nil {
		return PublicLink{}, err
	}
	if row.RevokedAt != nil || !row.ExpiresAt.After(s.now()) {
		return PublicLink{}, ErrLinkGone
	}
	if row.MaxDownloads != nil && row.DownloadCount >= *row.MaxDownloads {
		return PublicLink{}, ErrLinkGone
	}
	role, err := s.store.UserRole(ctx, row.CreatedBy)
	if err != nil {
		if errors.Is(err, ErrUserNotFound) {
			return PublicLink{}, ErrLinkGone
		}
		return PublicLink{}, err
	}
	creator := Actor{ID: row.CreatedBy, Role: role}
	grants, err := s.grantsFor(ctx, creator)
	if err != nil {
		return PublicLink{}, err
	}
	if levelOf(creator, grants, row.Path) < linkCreateLevel {
		return PublicLink{}, ErrLinkGone
	}
	info, err := s.tree.lstat(row.Path)
	if err != nil {
		if errors.Is(err, ErrNotFound) {
			return PublicLink{}, ErrLinkGone
		}
		return PublicLink{}, err
	}
	return PublicLink{
		row:           row,
		Name:          baseRel(row.Path),
		IsDir:         info.IsDir(),
		NeedsPassword: row.PasswordHash != "",
		ExpiresAt:     row.ExpiresAt,
	}, nil
}

// CheckLinkPassword verifies a password attempt for a protected link.
func (s *Service) CheckLinkPassword(link PublicLink, password string) bool {
	if link.row.PasswordHash == "" {
		return true
	}
	if password == "" || len(password) > maxLinkPassword {
		return false
	}
	return verifyLinkPassword(password, link.row.PasswordHash)
}

// UnlockProof is the cookie value proving the password was entered.
func (s *Service) UnlockProof(link PublicLink) string {
	return unlockProof(s.opts.Secret, link.row.ID, link.row.PasswordHash)
}

// Unlocked reports whether the link may be used with the presented proof.
func (s *Service) Unlocked(link PublicLink, proof string) bool {
	if link.row.PasswordHash == "" {
		return true
	}
	return validUnlockProof(s.opts.Secret, link.row.ID, link.row.PasswordHash, proof)
}

// linkTarget maps a path inside a link to a path in the shared space. sub is
// validated like any other client path, so it cannot climb out of the linked
// folder; a file link has nothing below it.
func linkTarget(link PublicLink, rawSub string) (string, string, error) {
	sub, err := CleanRel(rawSub)
	if err != nil {
		return "", "", err
	}
	if !link.IsDir {
		if sub != "" {
			return "", "", ErrNotFound
		}
		return link.row.Path, "", nil
	}
	if sub == "" {
		return link.row.Path, "", nil
	}
	return link.row.Path + "/" + sub, sub, nil
}

// PublicEntry is one row of a public folder listing. Path is relative to the
// link, never to the shared space.
type PublicEntry struct {
	Name       string
	Path       string
	IsDir      bool
	Size       int64
	ModifiedAt time.Time
}

// PublicList lists a folder inside a folder link. The caller must have checked
// Unlocked first.
func (s *Service) PublicList(link PublicLink, rawSub string) (string, []PublicEntry, error) {
	if !link.IsDir {
		return "", nil, ErrNotDir
	}
	target, sub, err := linkTarget(link, rawSub)
	if err != nil {
		return "", nil, err
	}
	infos, err := s.tree.readDir(target)
	if err != nil {
		return "", nil, err
	}
	entries := make([]PublicEntry, 0, len(infos))
	for _, info := range infos {
		entry := PublicEntry{Name: info.Name(), Path: joinRel(sub, info.Name()), IsDir: info.IsDir(), ModifiedAt: info.ModTime().UTC()}
		if !info.IsDir() {
			entry.Size = info.Size()
		}
		entries = append(entries, entry)
	}
	sort.Slice(entries, func(i, j int) bool {
		if entries[i].IsDir != entries[j].IsDir {
			return entries[i].IsDir
		}
		return strings.ToLower(entries[i].Name) < strings.ToLower(entries[j].Name)
	})
	if len(entries) > maxListEntries {
		entries = entries[:maxListEntries]
	}
	return sub, entries, nil
}

// PublicStat describes the file a download would serve, without counting it.
func (s *Service) PublicStat(link PublicLink, rawSub string) (os.FileInfo, error) {
	target, _, err := linkTarget(link, rawSub)
	if err != nil {
		return nil, err
	}
	return s.tree.lstat(target)
}

// PublicOpen opens a file for download through a link. When count is true the
// download is charged against the link's allowance first; a link that has run
// out fails with ErrLinkGone.
func (s *Service) PublicOpen(ctx context.Context, link PublicLink, rawSub string, count bool) (*os.File, os.FileInfo, error) {
	target, _, err := linkTarget(link, rawSub)
	if err != nil {
		return nil, nil, err
	}
	file, info, err := s.tree.open(target)
	if err != nil {
		return nil, nil, err
	}
	if count {
		ok, err := s.store.CountDownload(ctx, link.row.ID)
		if err != nil || !ok {
			file.Close()
			if err == nil {
				err = ErrLinkGone
			}
			return nil, nil, err
		}
	}
	return file, info, nil
}
