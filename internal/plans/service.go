package plans

import (
	"context"
	"errors"

	"necipdrive/internal/domain"

	"github.com/jackc/pgx/v5/pgxpool"
)

type Service struct {
	db *pgxpool.Pool
}

func NewService(db *pgxpool.Pool) *Service {
	return &Service{db: db}
}

func (s *Service) List(ctx context.Context) ([]domain.Plan, error) {
	rows, err := s.db.Query(ctx, `select code, name, quota_bytes, price_cents, billing_term, active from plans where active = true order by quota_bytes asc`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var items []domain.Plan
	for rows.Next() {
		var item domain.Plan
		if err := rows.Scan(&item.Code, &item.Name, &item.QuotaBytes, &item.PriceCents, &item.BillingTerm, &item.Active); err != nil {
			return nil, err
		}
		items = append(items, item)
	}
	return items, rows.Err()
}

func (s *Service) Assign(ctx context.Context, actorRole, targetUserID, planCode string) error {
	if actorRole != "admin" {
		return errors.New("admin role required")
	}
	_, err := s.db.Exec(ctx, `
		update users u
		set plan_code = p.code, quota_bytes = p.quota_bytes
		from plans p
		where u.id = $1::uuid and p.code = $2`,
		targetUserID, planCode,
	)
	return err
}
