package sharedspace

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"sort"
	"strings"
	"sync"
	"time"
	"unicode/utf8"
)

var (
	// ErrForbidden is returned when the caller lacks the needed permission. It
	// is decided before the disk is touched, so it never reveals whether a
	// path exists.
	ErrForbidden    = errors.New("bu işlem için yetkiniz yok")
	ErrInvalidInput = errors.New("geçersiz istek")
	ErrQuotaFull    = errors.New("Ortak Paylaşım alanı dolu")
	ErrTooLarge     = errors.New("dosya izin verilen boyutu aşıyor")
	ErrLinkGone     = errors.New("paylaşım bağlantısı artık geçerli değil")
	ErrLinkLocked   = errors.New("paylaşım bağlantısı şifre korumalı")
	// ErrBusy is returned when a path belongs to a torrent that is still
	// downloading or seeding.
	ErrBusy = errors.New("bu öğe etkin bir torrente ait; önce torrenti kaldırın veya tamamlanmasını bekleyin")
)

const (
	roleAdmin          = "admin"
	maxGroupNameBytes  = 80
	minLinkPassword    = 4
	maxLinkPassword    = 128
	defaultLinkTTL     = 7 * 24 * time.Hour
	minLinkTTL         = time.Minute
	maxListEntries     = 5000
	linkCreateLevel    = LevelWrite
	maxLinkMaxDownload = 1_000_000
)

// Actor is the authenticated caller.
type Actor struct {
	ID   string
	Role string
}

func (a Actor) IsAdmin() bool { return a.Role == roleAdmin }

// Mailer is the part of the mail service the quota warning needs.
type Mailer interface {
	Send(ctx context.Context, to, subject, body string) error
}

// Options configure a Service.
type Options struct {
	Dir            string
	QuotaBytes     int64 // 0 = bounded only by the disk
	WarnPercent    int
	MaxUploadBytes int64
	LinkMaxTTL     time.Duration
	// Secret signs the unlock cookie of password-protected links.
	Secret    string
	DiskSpace func(path string) (total, free int64, err error)
	Mailer    Mailer
	Now       func() time.Time
}

// Service is the shared space: files on disk plus grants, groups and links.
type Service struct {
	opts      Options
	store     Store
	tree      *tree
	mail      Mailer
	now       func() time.Time
	diskSpace func(path string) (total, free int64, err error)

	usageMu    sync.Mutex
	usageBytes int64
	usageAt    time.Time
	usageValid bool

	notifyMu       sync.Mutex
	notifiedLevel  string
	notifiedLoaded bool

	// writeMu serialises operations that change the tree's shape, so the
	// "does the destination exist" check and the rename that follows it are
	// not interleaved with another writer.
	writeMu sync.Mutex

	hooksMu sync.RWMutex
	hooks   Hooks
}

// Hooks connect the shared space to the torrent client without either package
// importing the other's internals.
type Hooks struct {
	// Busy reports whether a path belongs to a torrent still in progress.
	Busy func(rel string) bool
	// PathChanged is called after a path was renamed away or deleted.
	PathChanged func(rel string)
}

// SetHooks installs the hooks. Call it before serving requests.
func (s *Service) SetHooks(hooks Hooks) {
	s.hooksMu.Lock()
	s.hooks = hooks
	s.hooksMu.Unlock()
}

func (s *Service) busy(rel string) bool {
	s.hooksMu.RLock()
	busy := s.hooks.Busy
	s.hooksMu.RUnlock()
	return busy != nil && busy(rel)
}

func (s *Service) pathChanged(rel string) {
	s.hooksMu.RLock()
	changed := s.hooks.PathChanged
	s.hooksMu.RUnlock()
	if changed != nil {
		changed(rel)
	}
}

// New opens (and creates if needed) the shared folder.
func New(store Store, opts Options) (*Service, error) {
	if strings.TrimSpace(opts.Dir) == "" {
		return nil, errors.New("sharedspace: directory is required")
	}
	if opts.Secret == "" {
		return nil, errors.New("sharedspace: secret is required")
	}
	if opts.WarnPercent < minWarnPercent || opts.WarnPercent > maxWarnPercent {
		opts.WarnPercent = defaultWarnPercent
	}
	if opts.LinkMaxTTL <= 0 {
		opts.LinkMaxTTL = 30 * 24 * time.Hour
	}
	if opts.Now == nil {
		opts.Now = time.Now
	}
	if opts.DiskSpace == nil {
		opts.DiskSpace = func(string) (int64, int64, error) { return 0, 0, errors.New("disk space unavailable") }
	}
	t, err := openTree(opts.Dir)
	if err != nil {
		return nil, fmt.Errorf("sharedspace: %w", err)
	}
	return &Service{opts: opts, store: store, tree: t, mail: opts.Mailer, now: opts.Now, diskSpace: opts.DiskSpace}, nil
}

// Close releases the directory handle.
func (s *Service) Close() error { return s.tree.close() }

// Dir is the absolute path of the shared folder.
func (s *Service) Dir() string { return s.tree.dir }

// Entry is a file or folder in the shared space.
type Entry struct {
	Name       string    `json:"name"`
	Path       string    `json:"path"`
	IsDir      bool      `json:"isDir"`
	Size       int64     `json:"size"`
	ModifiedAt time.Time `json:"modifiedAt"`
	// Level is the caller's permission on this entry: none (may only pass
	// through it), read, write or admin.
	Level string `json:"level"`
}

// Listing is the content of one folder as the caller may see it.
type Listing struct {
	Path    string  `json:"path"`
	Level   string  `json:"level"`
	Entries []Entry `json:"entries"`
}

func entryFromInfo(dir string, info os.FileInfo, level Level) Entry {
	entry := Entry{
		Name:       info.Name(),
		Path:       joinRel(dir, info.Name()),
		IsDir:      info.IsDir(),
		ModifiedAt: info.ModTime().UTC(),
		Level:      level.String(),
	}
	if !info.IsDir() {
		entry.Size = info.Size()
	}
	return entry
}

// grantsFor loads the caller's grants; administrators need none.
func (s *Service) grantsFor(ctx context.Context, actor Actor) ([]UserGrant, error) {
	if actor.IsAdmin() {
		return nil, nil
	}
	if actor.ID == "" {
		return nil, ErrForbidden
	}
	return s.store.UserGrants(ctx, actor.ID)
}

func levelOf(actor Actor, grants []UserGrant, rel string) Level {
	if actor.IsAdmin() {
		return LevelAdmin
	}
	return effectiveLevel(grants, rel)
}

// require validates rawPath and checks the caller holds at least need on it.
func (s *Service) require(ctx context.Context, actor Actor, rawPath string, need Level) (string, Level, error) {
	rel, err := CleanRel(rawPath)
	if err != nil {
		return "", LevelNone, err
	}
	grants, err := s.grantsFor(ctx, actor)
	if err != nil {
		return "", LevelNone, err
	}
	level := levelOf(actor, grants, rel)
	if level < need {
		return "", level, ErrForbidden
	}
	return rel, level, nil
}

// Access summarises what the caller may do, for clients deciding whether to
// show the shared space at all.
type Access struct {
	// Available is true when the caller can see at least one folder.
	Available bool   `json:"available"`
	RootLevel string `json:"rootLevel"`
	// MaxLevel is the best permission held anywhere in the tree.
	MaxLevel string `json:"maxLevel"`
	Admin    bool   `json:"admin"`
}

func (s *Service) Access(ctx context.Context, actor Actor) (Access, error) {
	if actor.IsAdmin() {
		return Access{Available: true, RootLevel: LevelAdmin.String(), MaxLevel: LevelAdmin.String(), Admin: true}, nil
	}
	grants, err := s.grantsFor(ctx, actor)
	if err != nil {
		if errors.Is(err, ErrForbidden) {
			return Access{RootLevel: LevelNone.String(), MaxLevel: LevelNone.String()}, nil
		}
		return Access{}, err
	}
	best := highestLevel(grants)
	return Access{
		Available: best > LevelNone,
		RootLevel: effectiveLevel(grants, "").String(),
		MaxLevel:  best.String(),
	}, nil
}

// List returns the content of a folder. A caller without a grant on the folder
// itself still sees the sub-folders that lead to something granted, and
// nothing else; a caller with no relevant grant gets ErrForbidden.
func (s *Service) List(ctx context.Context, actor Actor, rawPath string) (Listing, error) {
	rel, err := CleanRel(rawPath)
	if err != nil {
		return Listing{}, err
	}
	grants, err := s.grantsFor(ctx, actor)
	if err != nil {
		return Listing{}, err
	}
	level := levelOf(actor, grants, rel)
	var passThrough map[string]bool
	if level == LevelNone {
		passThrough = traverseChildren(grants, rel)
		if len(passThrough) == 0 {
			return Listing{}, ErrForbidden
		}
	}
	infos, err := s.tree.readDir(rel)
	if err != nil {
		if level == LevelNone {
			// Do not confirm or deny the existence of a path the caller has
			// no grant on.
			return Listing{}, ErrForbidden
		}
		return Listing{}, err
	}
	out := Listing{Path: rel, Level: level.String(), Entries: make([]Entry, 0, len(infos))}
	for _, info := range infos {
		childLevel := level
		if level == LevelNone {
			if !info.IsDir() || !passThrough[info.Name()] {
				continue
			}
			childLevel = effectiveLevel(grants, joinRel(rel, info.Name()))
		} else if !actor.IsAdmin() {
			if deeper := effectiveLevel(grants, joinRel(rel, info.Name())); deeper > childLevel {
				childLevel = deeper
			}
		}
		out.Entries = append(out.Entries, entryFromInfo(rel, info, childLevel))
	}
	sort.Slice(out.Entries, func(i, j int) bool {
		a, b := out.Entries[i], out.Entries[j]
		if a.IsDir != b.IsDir {
			return a.IsDir
		}
		return strings.ToLower(a.Name) < strings.ToLower(b.Name)
	})
	if len(out.Entries) > maxListEntries {
		out.Entries = out.Entries[:maxListEntries]
	}
	return out, nil
}

// Open returns a file for reading. The caller must close it.
func (s *Service) Open(ctx context.Context, actor Actor, rawPath string) (*os.File, os.FileInfo, error) {
	rel, _, err := s.require(ctx, actor, rawPath, LevelRead)
	if err != nil {
		return nil, nil, err
	}
	if rel == "" {
		return nil, nil, ErrIsDir
	}
	return s.tree.open(rel)
}

// CreateFolder makes a new folder inside rawDir.
func (s *Service) CreateFolder(ctx context.Context, actor Actor, rawDir, name string) (Entry, error) {
	dir, level, err := s.require(ctx, actor, rawDir, LevelWrite)
	if err != nil {
		return Entry{}, err
	}
	name = strings.TrimSpace(name)
	rel, err := childPath(dir, name)
	if err != nil {
		return Entry{}, err
	}
	s.writeMu.Lock()
	defer s.writeMu.Unlock()
	if err := s.tree.mkdir(rel); err != nil {
		return Entry{}, err
	}
	info, err := s.tree.lstat(rel)
	if err != nil {
		return Entry{}, err
	}
	return entryFromInfo(dir, info, level), nil
}

// childPath validates name and joins it under dir, refusing the reserved
// internal directory.
func childPath(dir, name string) (string, error) {
	if err := ValidName(name); err != nil {
		return "", err
	}
	if !utf8.ValidString(name) || strings.TrimSpace(name) != name {
		return "", ErrInvalidName
	}
	rel, err := CleanRel(joinRel(dir, name))
	if err != nil {
		return "", ErrInvalidName
	}
	return rel, nil
}

// Upload stores body as a new file named name inside rawDir. size is the
// declared length, or -1 when unknown. The data is written to a staging file
// first and only moved into place once it is complete and within the quota.
func (s *Service) Upload(ctx context.Context, actor Actor, rawDir, name string, body io.Reader, size int64, overwrite bool) (Entry, error) {
	dir, level, err := s.require(ctx, actor, rawDir, LevelWrite)
	if err != nil {
		return Entry{}, err
	}
	name = strings.TrimSpace(name)
	rel, err := childPath(dir, name)
	if err != nil {
		return Entry{}, err
	}
	parent, err := s.tree.lstat(dir)
	if err != nil {
		return Entry{}, err
	}
	if !parent.IsDir() {
		return Entry{}, ErrNotDir
	}
	if overwrite && s.busy(rel) {
		return Entry{}, ErrBusy
	}
	quota, err := s.Quota(ctx)
	if err != nil {
		return Entry{}, err
	}
	if quota.Level == QuotaFull {
		return Entry{}, ErrQuotaFull
	}
	limit := quota.FreeBytes
	tooLarge := ErrQuotaFull
	if s.opts.MaxUploadBytes > 0 && s.opts.MaxUploadBytes < limit {
		limit = s.opts.MaxUploadBytes
		tooLarge = ErrTooLarge
	}
	if size > limit {
		return Entry{}, tooLarge
	}

	temp, tempName, err := s.tree.createTemp()
	if err != nil {
		return Entry{}, err
	}
	committed := false
	defer func() {
		if !committed {
			s.tree.removeTemp(tempName)
		}
	}()
	// Read one byte past the limit to tell "exactly at the limit" from "over".
	written, copyErr := io.Copy(temp, io.LimitReader(body, limit+1))
	closeErr := temp.Close()
	if copyErr != nil {
		return Entry{}, copyErr
	}
	if closeErr != nil {
		return Entry{}, closeErr
	}
	if written > limit {
		return Entry{}, tooLarge
	}
	if size >= 0 && written != size {
		return Entry{}, fmt.Errorf("%w: yükleme yarıda kesildi", ErrInvalidInput)
	}
	if err := ctx.Err(); err != nil {
		return Entry{}, err
	}

	s.writeMu.Lock()
	err = s.tree.commit(tempName, rel, overwrite)
	s.writeMu.Unlock()
	if err != nil {
		return Entry{}, err
	}
	committed = true
	s.InvalidateUsage()
	info, err := s.tree.lstat(rel)
	if err != nil {
		return Entry{}, err
	}
	return entryFromInfo(dir, info, level), nil
}

// Rename changes the name of an entry within its folder. It needs write access
// on the containing folder: a grant on "A/B" does not allow renaming "B" itself.
func (s *Service) Rename(ctx context.Context, actor Actor, rawPath, newName string) (Entry, error) {
	rel, err := CleanRel(rawPath)
	if err != nil {
		return Entry{}, err
	}
	if rel == "" {
		return Entry{}, ErrInvalidPath
	}
	dir, level, err := s.require(ctx, actor, parentRel(rel), LevelWrite)
	if err != nil {
		return Entry{}, err
	}
	newName = strings.TrimSpace(newName)
	target, err := childPath(dir, newName)
	if err != nil {
		return Entry{}, err
	}
	if target == rel {
		info, err := s.tree.lstat(rel)
		if err != nil {
			return Entry{}, err
		}
		return entryFromInfo(dir, info, level), nil
	}
	if s.busy(rel) || s.busy(target) {
		return Entry{}, ErrBusy
	}
	s.writeMu.Lock()
	defer s.writeMu.Unlock()
	if err := s.tree.rename(rel, target); err != nil {
		return Entry{}, err
	}
	// Grants and links follow the folder they were issued for.
	if err := s.store.RenamePath(ctx, rel, target); err != nil {
		// Without this the grants would point at a name that no longer exists
		// and could be inherited by whatever is created there next; undo.
		_ = s.tree.rename(target, rel)
		return Entry{}, err
	}
	s.pathChanged(rel)
	info, err := s.tree.lstat(target)
	if err != nil {
		return Entry{}, err
	}
	return entryFromInfo(dir, info, level), nil
}

// Delete removes a file or a folder with everything in it. Like Rename, it
// needs write access on the containing folder.
func (s *Service) Delete(ctx context.Context, actor Actor, rawPath string) error {
	rel, err := CleanRel(rawPath)
	if err != nil {
		return err
	}
	if rel == "" {
		return ErrInvalidPath
	}
	if _, _, err := s.require(ctx, actor, parentRel(rel), LevelWrite); err != nil {
		return err
	}
	if s.busy(rel) {
		return ErrBusy
	}
	s.writeMu.Lock()
	defer s.writeMu.Unlock()
	if _, err := s.tree.lstat(rel); err != nil {
		return err
	}
	// Access records go first: if removing the files then fails half-way, the
	// leftovers are at least no longer reachable through stale grants or links.
	if err := s.store.DeletePath(ctx, rel); err != nil {
		return err
	}
	err = s.tree.removeAll(rel)
	s.InvalidateUsage()
	if err == nil {
		s.pathChanged(rel)
	}
	return err
}

// ---- grants and groups (administrators only) ----

func (s *Service) ListGrants(ctx context.Context, actor Actor) ([]Grant, error) {
	if !actor.IsAdmin() {
		return nil, ErrForbidden
	}
	return s.store.ListGrants(ctx)
}

// GrantInput describes a grant to create or update.
type GrantInput struct {
	Path        string `json:"path"`
	SubjectType string `json:"subjectType"`
	UserID      string `json:"userId"`
	GroupID     string `json:"groupId"`
	Permission  string `json:"permission"`
}

func (s *Service) PutGrant(ctx context.Context, actor Actor, in GrantInput) (Grant, error) {
	if !actor.IsAdmin() {
		return Grant{}, ErrForbidden
	}
	rel, err := CleanRel(in.Path)
	if err != nil {
		return Grant{}, err
	}
	level, ok := parseGrantPermission(in.Permission)
	if !ok {
		return Grant{}, fmt.Errorf("%w: izin 'read' veya 'write' olmalı", ErrInvalidInput)
	}
	grant := Grant{Path: rel, SubjectType: strings.ToLower(strings.TrimSpace(in.SubjectType)), Permission: level.String()}
	switch grant.SubjectType {
	case SubjectUser:
		grant.UserID = strings.TrimSpace(in.UserID)
		if grant.UserID == "" {
			return Grant{}, fmt.Errorf("%w: kullanıcı seçilmedi", ErrInvalidInput)
		}
	case SubjectGroup:
		grant.GroupID = strings.TrimSpace(in.GroupID)
		if grant.GroupID == "" {
			return Grant{}, fmt.Errorf("%w: grup seçilmedi", ErrInvalidInput)
		}
	case SubjectEveryone:
	default:
		return Grant{}, fmt.Errorf("%w: özne 'user', 'group' veya 'everyone' olmalı", ErrInvalidInput)
	}
	// A grant on a path that does not exist would silently apply to whatever
	// is created there later.
	info, err := s.tree.lstat(rel)
	if err != nil {
		return Grant{}, err
	}
	if !info.IsDir() {
		return Grant{}, fmt.Errorf("%w: izin yalnızca klasörlere verilebilir", ErrInvalidInput)
	}
	return s.store.PutGrant(ctx, grant, actor.ID)
}

func (s *Service) DeleteGrant(ctx context.Context, actor Actor, id string) error {
	if !actor.IsAdmin() {
		return ErrForbidden
	}
	return s.store.DeleteGrant(ctx, strings.TrimSpace(id))
}

func (s *Service) ListGroups(ctx context.Context, actor Actor) ([]Group, error) {
	if !actor.IsAdmin() {
		return nil, ErrForbidden
	}
	return s.store.ListGroups(ctx)
}

func (s *Service) CreateGroup(ctx context.Context, actor Actor, name string) (Group, error) {
	if !actor.IsAdmin() {
		return Group{}, ErrForbidden
	}
	name = strings.TrimSpace(name)
	if name == "" || len(name) > maxGroupNameBytes || !utf8.ValidString(name) || strings.ContainsFunc(name, func(r rune) bool { return r < 0x20 || r == 0x7f }) {
		return Group{}, fmt.Errorf("%w: grup adı 1-%d karakter olmalı", ErrInvalidInput, maxGroupNameBytes)
	}
	return s.store.CreateGroup(ctx, name)
}

func (s *Service) DeleteGroup(ctx context.Context, actor Actor, id string) error {
	if !actor.IsAdmin() {
		return ErrForbidden
	}
	return s.store.DeleteGroup(ctx, strings.TrimSpace(id))
}

func (s *Service) AddGroupMember(ctx context.Context, actor Actor, groupID, userID string) error {
	if !actor.IsAdmin() {
		return ErrForbidden
	}
	if strings.TrimSpace(groupID) == "" || strings.TrimSpace(userID) == "" {
		return ErrInvalidInput
	}
	return s.store.AddGroupMember(ctx, strings.TrimSpace(groupID), strings.TrimSpace(userID))
}

func (s *Service) RemoveGroupMember(ctx context.Context, actor Actor, groupID, userID string) error {
	if !actor.IsAdmin() {
		return ErrForbidden
	}
	return s.store.RemoveGroupMember(ctx, strings.TrimSpace(groupID), strings.TrimSpace(userID))
}
