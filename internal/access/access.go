package access

import (
	"context"
	"errors"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

type Action string

const (
	ActionView       Action = "view"
	ActionComment    Action = "comment"
	ActionEdit       Action = "edit"
	ActionManage     Action = "manage"      // content_manager+: file ACL / share links
	ActionAdminDrive Action = "admin_drive" // manager+: membership & drive lifecycle
)

// Role levels (higher = more power).
var roleLevel = map[string]int{
	"viewer":          1,
	"commenter":       2,
	"contributor":     3,
	"editor":          3,
	"content_manager": 4,
	"manager":         5,
	"owner":           6,
}

func requiredLevel(action Action) int {
	switch action {
	case ActionView:
		return 1
	case ActionComment:
		return 2
	case ActionEdit:
		return 3
	case ActionManage:
		return 4
	case ActionAdminDrive:
		return 5
	default:
		return 99
	}
}

type Service struct {
	db *pgxpool.Pool
}

func New(db *pgxpool.Pool) *Service {
	return &Service{db: db}
}

func (s *Service) Can(ctx context.Context, userID, userRole, entryID string, action Action) (bool, error) {
	if userRole == "admin" {
		return true, nil
	}
	level, err := s.effectiveLevel(ctx, userID, entryID)
	if err != nil {
		return false, err
	}
	return level >= requiredLevel(action), nil
}

func (s *Service) Require(ctx context.Context, userID, userRole, entryID string, action Action) error {
	ok, err := s.Can(ctx, userID, userRole, entryID, action)
	if err != nil {
		return err
	}
	if !ok {
		return errors.New("forbidden")
	}
	return nil
}

func (s *Service) effectiveLevel(ctx context.Context, userID, entryID string) (int, error) {
	var ownerID, driveID string
	var driveKind, memberRole *string
	err := s.db.QueryRow(ctx, `
		select f.user_id::text,
		       coalesce(f.drive_id::text, ''),
		       d.kind,
		       dm.role
		from file_entries f
		left join drives d on d.id = f.drive_id
		left join drive_members dm on dm.drive_id = f.drive_id and dm.user_id = $2::uuid
		where f.id = $1::uuid`,
		entryID, userID,
	).Scan(&ownerID, &driveID, &driveKind, &memberRole)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return 0, errors.New("file not found")
		}
		return 0, err
	}

	best := 0
	if ownerID == userID {
		best = roleLevel["owner"]
	}
	if driveKind != nil && *driveKind == "personal" && ownerID == userID {
		best = max(best, roleLevel["owner"])
	}
	if memberRole != nil {
		best = max(best, roleLevel[*memberRole])
	}

	// Walk ancestors for inherited file_permissions.
	aclRole, err := s.aclRoleOnAncestors(ctx, userID, entryID)
	if err != nil {
		return 0, err
	}
	if aclRole != "" {
		best = max(best, roleLevel[aclRole])
	}
	return best, nil
}

func (s *Service) aclRoleOnAncestors(ctx context.Context, userID, entryID string) (string, error) {
	var role string
	err := s.db.QueryRow(ctx, `
		with recursive chain as (
			select id, parent_id from file_entries where id = $1::uuid
			union all
			select f.id, f.parent_id from file_entries f join chain c on f.id = c.parent_id
		)
		select p.role
		from chain c
		join file_permissions p on p.entry_id = c.id and p.grantee_user_id = $2::uuid
		order by
			case p.role
				when 'editor' then 3
				when 'commenter' then 2
				when 'viewer' then 1
				else 0
			end desc
		limit 1`,
		entryID, userID,
	).Scan(&role)
	if errors.Is(err, pgx.ErrNoRows) {
		return "", nil
	}
	return role, err
}

func (s *Service) CanDrive(ctx context.Context, userID, userRole, driveID string, action Action) (bool, error) {
	if userRole == "admin" {
		return true, nil
	}
	var ownerID, kind, memberRole string
	var hasMember bool
	err := s.db.QueryRow(ctx, `
		select d.owner_user_id::text, d.kind, coalesce(dm.role, '')
		from drives d
		left join drive_members dm on dm.drive_id = d.id and dm.user_id = $2::uuid
		where d.id = $1::uuid`,
		driveID, userID,
	).Scan(&ownerID, &kind, &memberRole)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return false, errors.New("drive not found")
		}
		return false, err
	}
	hasMember = memberRole != ""
	level := 0
	if ownerID == userID {
		level = roleLevel["owner"]
	}
	if hasMember {
		level = max(level, roleLevel[memberRole])
	}
	if kind == "personal" && ownerID == userID {
		level = roleLevel["owner"]
	}
	return level >= requiredLevel(action), nil
}

func max(a, b int) int {
	if a > b {
		return a
	}
	return b
}
