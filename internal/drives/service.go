package drives

import (
	"context"
	"errors"
	"strings"

	"necipdrive/internal/access"
	"necipdrive/internal/domain"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

type Service struct {
	db     *pgxpool.Pool
	access *access.Service
}

func NewService(db *pgxpool.Pool, accessSvc *access.Service) *Service {
	return &Service{db: db, access: accessSvc}
}

func (s *Service) List(ctx context.Context, userID string) ([]domain.Drive, error) {
	rows, err := s.db.Query(ctx, `
		select d.id::text, d.kind, d.name, d.owner_user_id::text, coalesce(d.root_entry_id::text, ''), dm.role, d.created_at
		from drives d
		join drive_members dm on dm.drive_id = d.id and dm.user_id = $1::uuid
		where d.kind = 'shared' or (d.kind = 'personal' and d.owner_user_id = $1::uuid)
		order by d.kind desc, lower(d.name)`, userID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := make([]domain.Drive, 0)
	for rows.Next() {
		var d domain.Drive
		if err := rows.Scan(&d.ID, &d.Kind, &d.Name, &d.OwnerUserID, &d.RootEntryID, &d.MyRole, &d.CreatedAt); err != nil {
			return nil, err
		}
		out = append(out, d)
	}
	return out, rows.Err()
}

func (s *Service) PersonalDriveID(ctx context.Context, userID string) (string, error) {
	var id string
	err := s.db.QueryRow(ctx, `select id::text from drives where owner_user_id = $1::uuid and kind = 'personal'`, userID).Scan(&id)
	return id, err
}

func (s *Service) CreateShared(ctx context.Context, owner domain.User, name string) (*domain.Drive, error) {
	name = strings.TrimSpace(name)
	if name == "" {
		return nil, errors.New("name is required")
	}
	tx, err := s.db.Begin(ctx)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback(ctx)

	driveID := uuid.NewString()
	rootID := uuid.NewString()
	if _, err := tx.Exec(ctx, `
		insert into drives (id, kind, name, owner_user_id)
		values ($1::uuid, 'shared', $2, $3::uuid)`, driveID, name, owner.ID); err != nil {
		return nil, err
	}
	if _, err := tx.Exec(ctx, `
		insert into file_entries (id, user_id, drive_id, parent_id, name, kind, storage_key, size_bytes, mime_type)
		values ($1::uuid, $2::uuid, $3::uuid, null, $4, 'folder', $5, 0, 'inode/directory')`,
		rootID, owner.ID, driveID, name, "drive/"+driveID); err != nil {
		return nil, err
	}
	if _, err := tx.Exec(ctx, `update drives set root_entry_id = $1::uuid where id = $2::uuid`, rootID, driveID); err != nil {
		return nil, err
	}
	if _, err := tx.Exec(ctx, `
		insert into drive_members (drive_id, user_id, role) values ($1::uuid, $2::uuid, 'manager')`, driveID, owner.ID); err != nil {
		return nil, err
	}
	if err := tx.Commit(ctx); err != nil {
		return nil, err
	}
	return &domain.Drive{
		ID: driveID, Kind: "shared", Name: name, OwnerUserID: owner.ID,
		RootEntryID: rootID, MyRole: "manager",
	}, nil
}

func (s *Service) Get(ctx context.Context, userID, userRole, driveID string) (*domain.Drive, error) {
	ok, err := s.access.CanDrive(ctx, userID, userRole, driveID, access.ActionView)
	if err != nil || !ok {
		if err == nil {
			err = errors.New("forbidden")
		}
		return nil, err
	}
	var d domain.Drive
	err = s.db.QueryRow(ctx, `
		select d.id::text, d.kind, d.name, d.owner_user_id::text, coalesce(d.root_entry_id::text, ''),
		       coalesce(dm.role, ''), d.created_at
		from drives d
		left join drive_members dm on dm.drive_id = d.id and dm.user_id = $2::uuid
		where d.id = $1::uuid`, driveID, userID,
	).Scan(&d.ID, &d.Kind, &d.Name, &d.OwnerUserID, &d.RootEntryID, &d.MyRole, &d.CreatedAt)
	if err != nil {
		return nil, err
	}
	return &d, nil
}

func (s *Service) Rename(ctx context.Context, userID, userRole, driveID, name string) error {
	ok, err := s.access.CanDrive(ctx, userID, userRole, driveID, access.ActionAdminDrive)
	if err != nil {
		return err
	}
	if !ok {
		return errors.New("forbidden")
	}
	name = strings.TrimSpace(name)
	if name == "" {
		return errors.New("name is required")
	}
	_, err = s.db.Exec(ctx, `update drives set name = $1 where id = $2::uuid and kind = 'shared'`, name, driveID)
	return err
}

func (s *Service) Delete(ctx context.Context, userID, userRole, driveID string) error {
	ok, err := s.access.CanDrive(ctx, userID, userRole, driveID, access.ActionAdminDrive)
	if err != nil {
		return err
	}
	if !ok {
		return errors.New("forbidden")
	}
	var kind string
	if err := s.db.QueryRow(ctx, `select kind from drives where id = $1::uuid`, driveID).Scan(&kind); err != nil {
		return err
	}
	if kind != "shared" {
		return errors.New("cannot delete personal drive")
	}
	_, err = s.db.Exec(ctx, `delete from drives where id = $1::uuid`, driveID)
	return err
}

func (s *Service) ListMembers(ctx context.Context, userID, userRole, driveID string) ([]domain.DriveMember, error) {
	ok, err := s.access.CanDrive(ctx, userID, userRole, driveID, access.ActionView)
	if err != nil || !ok {
		if err == nil {
			err = errors.New("forbidden")
		}
		return nil, err
	}
	rows, err := s.db.Query(ctx, `
		select u.id::text, u.email, u.display_name, dm.role, dm.created_at
		from drive_members dm
		join users u on u.id = dm.user_id
		where dm.drive_id = $1::uuid
		order by lower(u.email)`, driveID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := make([]domain.DriveMember, 0)
	for rows.Next() {
		var m domain.DriveMember
		if err := rows.Scan(&m.UserID, &m.Email, &m.DisplayName, &m.Role, &m.CreatedAt); err != nil {
			return nil, err
		}
		out = append(out, m)
	}
	return out, rows.Err()
}

func (s *Service) AddMember(ctx context.Context, actorID, actorRole, driveID, email, role string) error {
	ok, err := s.access.CanDrive(ctx, actorID, actorRole, driveID, access.ActionAdminDrive)
	if err != nil || !ok {
		if err == nil {
			err = errors.New("forbidden")
		}
		return err
	}
	role = normalizeDriveRole(role)
	if role == "" {
		return errors.New("invalid role")
	}
	var userID string
	err = s.db.QueryRow(ctx, `select id::text from users where email = $1`, strings.ToLower(strings.TrimSpace(email))).Scan(&userID)
	if errors.Is(err, pgx.ErrNoRows) {
		return errors.New("user not found")
	}
	if err != nil {
		return err
	}
	_, err = s.db.Exec(ctx, `
		insert into drive_members (drive_id, user_id, role) values ($1::uuid, $2::uuid, $3)
		on conflict (drive_id, user_id) do update set role = excluded.role`, driveID, userID, role)
	if err != nil {
		return err
	}
	_, _ = s.db.Exec(ctx, `
		insert into notifications (user_id, kind, title, body, drive_id)
		values ($1::uuid, 'drive_invite', 'Ortak alana eklendiniz', $2, $3::uuid)`,
		userID, "Bir ortak alana üye olarak eklendiniz.", driveID)
	return nil
}

func (s *Service) RemoveMember(ctx context.Context, actorID, actorRole, driveID, memberUserID string) error {
	ok, err := s.access.CanDrive(ctx, actorID, actorRole, driveID, access.ActionAdminDrive)
	if err != nil || !ok {
		if err == nil {
			err = errors.New("forbidden")
		}
		return err
	}
	var ownerID string
	_ = s.db.QueryRow(ctx, `select owner_user_id::text from drives where id = $1::uuid`, driveID).Scan(&ownerID)
	if memberUserID == ownerID {
		return errors.New("cannot remove drive owner")
	}
	_, err = s.db.Exec(ctx, `delete from drive_members where drive_id = $1::uuid and user_id = $2::uuid`, driveID, memberUserID)
	return err
}

func normalizeDriveRole(role string) string {
	switch strings.ToLower(strings.TrimSpace(role)) {
	case "viewer", "commenter", "contributor", "content_manager", "manager":
		return strings.ToLower(strings.TrimSpace(role))
	case "editor":
		return "contributor"
	default:
		return ""
	}
}
