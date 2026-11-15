package sharedspace

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"
)

// Grant gives a subject read or write access to a path and everything below it.
type Grant struct {
	ID          string    `json:"id"`
	Path        string    `json:"path"`
	SubjectType string    `json:"subjectType"`
	UserID      string    `json:"userId,omitempty"`
	UserEmail   string    `json:"userEmail,omitempty"`
	UserName    string    `json:"userName,omitempty"`
	GroupID     string    `json:"groupId,omitempty"`
	GroupName   string    `json:"groupName,omitempty"`
	Permission  string    `json:"permission"`
	CreatedAt   time.Time `json:"createdAt"`
}

// Group is a named set of users that grants can be issued to.
type Group struct {
	ID        string        `json:"id"`
	Name      string        `json:"name"`
	CreatedAt time.Time     `json:"createdAt"`
	Members   []GroupMember `json:"members"`
}

type GroupMember struct {
	UserID      string `json:"userId"`
	Email       string `json:"email"`
	DisplayName string `json:"displayName"`
}

// linkRow is a stored public link. The token itself is never stored.
type linkRow struct {
	ID            string
	TokenHash     string
	Path          string
	PasswordHash  string
	ExpiresAt     time.Time
	MaxDownloads  *int64
	DownloadCount int64
	CreatedBy     string
	CreatorEmail  string
	CreatedAt     time.Time
	RevokedAt     *time.Time
}

// Store is the persistence the shared space needs. The PostgreSQL
// implementation is used in production; tests may substitute an in-memory one.
type Store interface {
	// UserGrants returns every grant that applies to the user: direct ones,
	// those of the user's groups, and those issued to everyone.
	UserGrants(ctx context.Context, userID string) ([]UserGrant, error)
	UserRole(ctx context.Context, userID string) (string, error)

	ListGrants(ctx context.Context) ([]Grant, error)
	PutGrant(ctx context.Context, grant Grant, actorID string) (Grant, error)
	DeleteGrant(ctx context.Context, id string) error

	ListGroups(ctx context.Context) ([]Group, error)
	CreateGroup(ctx context.Context, name string) (Group, error)
	DeleteGroup(ctx context.Context, id string) error
	AddGroupMember(ctx context.Context, groupID, userID string) error
	RemoveGroupMember(ctx context.Context, groupID, userID string) error

	// RenamePath and DeletePath keep grants and links attached to the folder
	// they were issued for when it is renamed, and drop them when it is deleted
	// so a later folder of the same name does not inherit stale access.
	RenamePath(ctx context.Context, oldRel, newRel string) error
	DeletePath(ctx context.Context, rel string) error

	CreateLink(ctx context.Context, link linkRow) (linkRow, error)
	LinkByTokenHash(ctx context.Context, tokenHash string) (linkRow, error)
	ListLinks(ctx context.Context, userID string, all bool) ([]linkRow, error)
	RevokeLink(ctx context.Context, id, userID string, anyOwner bool) error
	// CountDownload atomically records one download and reports false when the
	// link's download allowance is already used up.
	CountDownload(ctx context.Context, id string) (bool, error)

	Setting(ctx context.Context, key string) (string, bool, error)
	SetSetting(ctx context.Context, key, value string) error
	// NotifyAdmins stores an in-app notification for every administrator and
	// returns their e-mail addresses.
	NotifyAdmins(ctx context.Context, kind, title, body string) ([]string, error)
}

var (
	ErrGrantNotFound = errors.New("izin kaydı bulunamadı")
	ErrGroupNotFound = errors.New("grup bulunamadı")
	ErrGroupExists   = errors.New("bu adda bir grup zaten var")
	ErrUserNotFound  = errors.New("kullanıcı bulunamadı")
	ErrLinkNotFound  = errors.New("paylaşım bağlantısı bulunamadı")
)

type pgStore struct {
	db *pgxpool.Pool
}

// NewPostgresStore returns the production Store.
func NewPostgresStore(db *pgxpool.Pool) Store { return &pgStore{db: db} }

func (s *pgStore) UserGrants(ctx context.Context, userID string) ([]UserGrant, error) {
	rows, err := s.db.Query(ctx, `
		select g.path, g.permission
		from shared_space_grants g
		where g.subject_type = 'everyone'
		   or (g.subject_type = 'user' and g.user_id = $1::uuid)
		   or (g.subject_type = 'group' and g.group_id in (
				select m.group_id from user_group_members m where m.user_id = $1::uuid))`, userID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []UserGrant
	for rows.Next() {
		var path, permission string
		if err := rows.Scan(&path, &permission); err != nil {
			return nil, err
		}
		level, ok := parseGrantPermission(permission)
		if !ok {
			continue
		}
		out = append(out, UserGrant{Path: path, Level: level})
	}
	return out, rows.Err()
}

func (s *pgStore) UserRole(ctx context.Context, userID string) (string, error) {
	var role string
	err := s.db.QueryRow(ctx, `select role from users where id = $1::uuid`, userID).Scan(&role)
	if errors.Is(err, pgx.ErrNoRows) {
		return "", ErrUserNotFound
	}
	return role, err
}

const grantSelect = `
	select g.id::text, g.path, g.subject_type,
	       coalesce(g.user_id::text, ''), coalesce(u.email, ''), coalesce(u.display_name, ''),
	       coalesce(g.group_id::text, ''), coalesce(gr.name, ''),
	       g.permission, g.created_at
	from shared_space_grants g
	left join users u on u.id = g.user_id
	left join user_groups gr on gr.id = g.group_id`

func scanGrant(row pgx.Row) (Grant, error) {
	var g Grant
	err := row.Scan(&g.ID, &g.Path, &g.SubjectType, &g.UserID, &g.UserEmail, &g.UserName, &g.GroupID, &g.GroupName, &g.Permission, &g.CreatedAt)
	return g, err
}

func (s *pgStore) ListGrants(ctx context.Context) ([]Grant, error) {
	rows, err := s.db.Query(ctx, grantSelect+` order by g.path, g.subject_type, g.created_at`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []Grant{}
	for rows.Next() {
		g, err := scanGrant(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, g)
	}
	return out, rows.Err()
}

func (s *pgStore) PutGrant(ctx context.Context, grant Grant, actorID string) (Grant, error) {
	var userID, groupID *string
	switch grant.SubjectType {
	case SubjectUser:
		userID = &grant.UserID
	case SubjectGroup:
		groupID = &grant.GroupID
	}
	var id string
	err := s.db.QueryRow(ctx, `
		insert into shared_space_grants (path, subject_type, user_id, group_id, permission, created_by_user_id)
		values ($1, $2, $3::uuid, $4::uuid, $5, $6::uuid)
		on conflict (path, subject_type,
			coalesce(user_id, '00000000-0000-0000-0000-000000000000'::uuid),
			coalesce(group_id, '00000000-0000-0000-0000-000000000000'::uuid))
		do update set permission = excluded.permission
		returning id::text`,
		grant.Path, grant.SubjectType, userID, groupID, grant.Permission, nullableUUID(actorID)).Scan(&id)
	if err != nil {
		if isForeignKeyViolation(err) || isInvalidUUID(err) {
			if grant.SubjectType == SubjectGroup {
				return Grant{}, ErrGroupNotFound
			}
			return Grant{}, ErrUserNotFound
		}
		return Grant{}, err
	}
	return scanGrant(s.db.QueryRow(ctx, grantSelect+` where g.id = $1::uuid`, id))
}

func (s *pgStore) DeleteGrant(ctx context.Context, id string) error {
	tag, err := s.db.Exec(ctx, `delete from shared_space_grants where id = $1::uuid`, id)
	if err != nil {
		if isInvalidUUID(err) {
			return ErrGrantNotFound
		}
		return err
	}
	if tag.RowsAffected() == 0 {
		return ErrGrantNotFound
	}
	return nil
}

func (s *pgStore) ListGroups(ctx context.Context) ([]Group, error) {
	rows, err := s.db.Query(ctx, `select id::text, name, created_at from user_groups order by lower(name)`)
	if err != nil {
		return nil, err
	}
	groups := []Group{}
	index := map[string]int{}
	for rows.Next() {
		var g Group
		if err := rows.Scan(&g.ID, &g.Name, &g.CreatedAt); err != nil {
			rows.Close()
			return nil, err
		}
		g.Members = []GroupMember{}
		index[g.ID] = len(groups)
		groups = append(groups, g)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return nil, err
	}
	members, err := s.db.Query(ctx, `
		select m.group_id::text, u.id::text, u.email, u.display_name
		from user_group_members m
		join users u on u.id = m.user_id
		order by lower(u.email)`)
	if err != nil {
		return nil, err
	}
	defer members.Close()
	for members.Next() {
		var groupID string
		var member GroupMember
		if err := members.Scan(&groupID, &member.UserID, &member.Email, &member.DisplayName); err != nil {
			return nil, err
		}
		if i, ok := index[groupID]; ok {
			groups[i].Members = append(groups[i].Members, member)
		}
	}
	return groups, members.Err()
}

func (s *pgStore) CreateGroup(ctx context.Context, name string) (Group, error) {
	g := Group{Name: name, Members: []GroupMember{}}
	err := s.db.QueryRow(ctx, `insert into user_groups (name) values ($1) returning id::text, created_at`, name).Scan(&g.ID, &g.CreatedAt)
	if err != nil {
		var pgErr *pgconn.PgError
		if errors.As(err, &pgErr) && pgErr.Code == "23505" {
			return Group{}, ErrGroupExists
		}
		return Group{}, err
	}
	return g, nil
}

func (s *pgStore) DeleteGroup(ctx context.Context, id string) error {
	tag, err := s.db.Exec(ctx, `delete from user_groups where id = $1::uuid`, id)
	if err != nil {
		if isInvalidUUID(err) {
			return ErrGroupNotFound
		}
		return err
	}
	if tag.RowsAffected() == 0 {
		return ErrGroupNotFound
	}
	return nil
}

func (s *pgStore) AddGroupMember(ctx context.Context, groupID, userID string) error {
	_, err := s.db.Exec(ctx, `
		insert into user_group_members (group_id, user_id) values ($1::uuid, $2::uuid)
		on conflict do nothing`, groupID, userID)
	if err != nil {
		if isForeignKeyViolation(err) || isInvalidUUID(err) {
			return ErrUserNotFound
		}
		return err
	}
	return nil
}

func (s *pgStore) RemoveGroupMember(ctx context.Context, groupID, userID string) error {
	_, err := s.db.Exec(ctx, `delete from user_group_members where group_id = $1::uuid and user_id = $2::uuid`, groupID, userID)
	if err != nil && isInvalidUUID(err) {
		return nil
	}
	return err
}

// likePrefix escapes rel for use as a LIKE prefix matching its descendants.
func likePrefix(rel string) string {
	replacer := strings.NewReplacer(`\`, `\\`, `%`, `\%`, `_`, `\_`)
	return replacer.Replace(rel) + "/%"
}

func (s *pgStore) RenamePath(ctx context.Context, oldRel, newRel string) error {
	if oldRel == "" {
		return nil
	}
	tx, err := s.db.Begin(ctx)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback(ctx) }()
	for _, table := range []string{"shared_space_grants", "shared_space_links"} {
		// A grant already present on the destination wins; the moved duplicate
		// is dropped instead of violating the uniqueness of (path, subject).
		if table == "shared_space_grants" {
			if _, err := tx.Exec(ctx, `
				delete from shared_space_grants g
				where (g.path = $1 or g.path like $2 escape '\')
				  and exists (
					select 1 from shared_space_grants d
					where d.path = $3 || substr(g.path, $4)
					  and d.subject_type = g.subject_type
					  and d.user_id is not distinct from g.user_id
					  and d.group_id is not distinct from g.group_id)`,
				oldRel, likePrefix(oldRel), newRel, len([]rune(oldRel))+1); err != nil {
				return err
			}
		}
		if _, err := tx.Exec(ctx, fmt.Sprintf(`
			update %s set path = $3 || substr(path, $4)
			where path = $1 or path like $2 escape '\'`, table),
			oldRel, likePrefix(oldRel), newRel, len([]rune(oldRel))+1); err != nil {
			return err
		}
	}
	return tx.Commit(ctx)
}

func (s *pgStore) DeletePath(ctx context.Context, rel string) error {
	if rel == "" {
		return nil
	}
	if _, err := s.db.Exec(ctx, `delete from shared_space_grants where path = $1 or path like $2 escape '\'`, rel, likePrefix(rel)); err != nil {
		return err
	}
	_, err := s.db.Exec(ctx, `
		update shared_space_links set revoked_at = now()
		where revoked_at is null and (path = $1 or path like $2 escape '\')`, rel, likePrefix(rel))
	return err
}

const linkSelect = `
	select l.id::text, l.token_hash, l.path, l.password_hash, l.expires_at, l.max_downloads,
	       l.download_count, l.created_by_user_id::text, coalesce(u.email, ''), l.created_at, l.revoked_at
	from shared_space_links l
	left join users u on u.id = l.created_by_user_id`

func scanLink(row pgx.Row) (linkRow, error) {
	var l linkRow
	err := row.Scan(&l.ID, &l.TokenHash, &l.Path, &l.PasswordHash, &l.ExpiresAt, &l.MaxDownloads,
		&l.DownloadCount, &l.CreatedBy, &l.CreatorEmail, &l.CreatedAt, &l.RevokedAt)
	return l, err
}

func (s *pgStore) CreateLink(ctx context.Context, link linkRow) (linkRow, error) {
	var id string
	err := s.db.QueryRow(ctx, `
		insert into shared_space_links (token_hash, path, password_hash, expires_at, max_downloads, created_by_user_id, created_at)
		values ($1, $2, $3, $4, $5, $6::uuid, $7)
		returning id::text`,
		link.TokenHash, link.Path, link.PasswordHash, link.ExpiresAt, link.MaxDownloads, link.CreatedBy, link.CreatedAt).Scan(&id)
	if err != nil {
		return linkRow{}, err
	}
	return scanLink(s.db.QueryRow(ctx, linkSelect+` where l.id = $1::uuid`, id))
}

func (s *pgStore) LinkByTokenHash(ctx context.Context, tokenHash string) (linkRow, error) {
	link, err := scanLink(s.db.QueryRow(ctx, linkSelect+` where l.token_hash = $1`, tokenHash))
	if errors.Is(err, pgx.ErrNoRows) {
		return linkRow{}, ErrLinkNotFound
	}
	return link, err
}

func (s *pgStore) ListLinks(ctx context.Context, userID string, all bool) ([]linkRow, error) {
	query := linkSelect + ` where l.revoked_at is null`
	args := []any{}
	if !all {
		query += ` and l.created_by_user_id = $1::uuid`
		args = append(args, userID)
	}
	rows, err := s.db.Query(ctx, query+` order by l.created_at desc limit 500`, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []linkRow{}
	for rows.Next() {
		link, err := scanLink(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, link)
	}
	return out, rows.Err()
}

func (s *pgStore) RevokeLink(ctx context.Context, id, userID string, anyOwner bool) error {
	query := `update shared_space_links set revoked_at = now() where id = $1::uuid and revoked_at is null`
	args := []any{id}
	if !anyOwner {
		query += ` and created_by_user_id = $2::uuid`
		args = append(args, userID)
	}
	tag, err := s.db.Exec(ctx, query, args...)
	if err != nil {
		if isInvalidUUID(err) {
			return ErrLinkNotFound
		}
		return err
	}
	if tag.RowsAffected() == 0 {
		return ErrLinkNotFound
	}
	return nil
}

func (s *pgStore) CountDownload(ctx context.Context, id string) (bool, error) {
	tag, err := s.db.Exec(ctx, `
		update shared_space_links set download_count = download_count + 1
		where id = $1::uuid and revoked_at is null
		  and (max_downloads is null or download_count < max_downloads)`, id)
	if err != nil {
		return false, err
	}
	return tag.RowsAffected() == 1, nil
}

func (s *pgStore) Setting(ctx context.Context, key string) (string, bool, error) {
	var value string
	err := s.db.QueryRow(ctx, `select value from app_settings where key = $1`, key).Scan(&value)
	if errors.Is(err, pgx.ErrNoRows) {
		return "", false, nil
	}
	return value, err == nil, err
}

func (s *pgStore) SetSetting(ctx context.Context, key, value string) error {
	_, err := s.db.Exec(ctx, `
		insert into app_settings (key, value, updated_at) values ($1, $2, now())
		on conflict (key) do update set value = excluded.value, updated_at = now()`, key, value)
	return err
}

func (s *pgStore) NotifyAdmins(ctx context.Context, kind, title, body string) ([]string, error) {
	rows, err := s.db.Query(ctx, `
		insert into notifications (user_id, kind, title, body)
		select u.id, $1, $2, $3 from users u where u.role = 'admin'
		returning (select email from users where id = notifications.user_id)`, kind, title, body)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var emails []string
	for rows.Next() {
		var email string
		if err := rows.Scan(&email); err != nil {
			return nil, err
		}
		emails = append(emails, email)
	}
	return emails, rows.Err()
}

func nullableUUID(value string) *string {
	if strings.TrimSpace(value) == "" {
		return nil
	}
	return &value
}

func isForeignKeyViolation(err error) bool {
	var pgErr *pgconn.PgError
	return errors.As(err, &pgErr) && pgErr.Code == "23503"
}

// isInvalidUUID matches "invalid input syntax for type uuid": a malformed id
// from a client is a not-found condition, not a server error.
func isInvalidUUID(err error) bool {
	var pgErr *pgconn.PgError
	return errors.As(err, &pgErr) && pgErr.Code == "22P02"
}
