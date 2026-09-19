package repository

import (
	"context"
	"database/sql"
	"errors"

	dbent "github.com/Wei-Shaw/sub2api/ent"
	"github.com/Wei-Shaw/sub2api/ent/accesslevel"
	"github.com/Wei-Shaw/sub2api/ent/group"
	"github.com/Wei-Shaw/sub2api/ent/user"
	"github.com/Wei-Shaw/sub2api/internal/service"
)

type accessLevelRepository struct {
	client *dbent.Client
	db     *sql.DB
}

func NewAccessLevelRepository(client *dbent.Client, db *sql.DB) service.AccessLevelRepository {
	return &accessLevelRepository{client: client, db: db}
}

func (r *accessLevelRepository) List(ctx context.Context) ([]service.AccessLevel, error) {
	rows, err := r.client.AccessLevel.Query().Order(dbent.Asc(accesslevel.FieldRank), dbent.Asc(accesslevel.FieldID)).All(ctx)
	if err != nil {
		return nil, err
	}
	out := make([]service.AccessLevel, 0, len(rows))
	for _, row := range rows {
		out = append(out, *accessLevelEntityToService(row))
	}
	return out, nil
}

func (r *accessLevelRepository) GetByID(ctx context.Context, id int64) (*service.AccessLevel, error) {
	row, err := r.client.AccessLevel.Query().Where(accesslevel.IDEQ(id)).Only(ctx)
	if err != nil {
		if dbent.IsNotFound(err) {
			return nil, service.ErrAccessLevelNotFound
		}
		return nil, err
	}
	return accessLevelEntityToService(row), nil
}

func (r *accessLevelRepository) Create(ctx context.Context, level *service.AccessLevel) error {
	row, err := r.client.AccessLevel.Create().
		SetName(level.Name).
		SetRank(level.Rank).
		SetBalanceThreshold(level.BalanceThreshold).
		SetPaygDiscountMultiplier(level.PaygDiscountMultiplier).
		Save(ctx)
	if err != nil {
		return err
	}
	*level = *accessLevelEntityToService(row)
	return nil
}

func (r *accessLevelRepository) Update(ctx context.Context, level *service.AccessLevel) error {
	row, err := r.client.AccessLevel.UpdateOneID(level.ID).
		SetName(level.Name).
		SetRank(level.Rank).
		SetBalanceThreshold(level.BalanceThreshold).
		SetPaygDiscountMultiplier(level.PaygDiscountMultiplier).
		Save(ctx)
	if err != nil {
		if dbent.IsNotFound(err) {
			return service.ErrAccessLevelNotFound
		}
		return err
	}
	*level = *accessLevelEntityToService(row)
	return nil
}

func (r *accessLevelRepository) Delete(ctx context.Context, id int64) error {
	n, err := r.client.AccessLevel.Delete().Where(accesslevel.IDEQ(id)).Exec(ctx)
	if err != nil {
		return err
	}
	if n == 0 {
		return service.ErrAccessLevelNotFound
	}
	return nil
}

func (r *accessLevelRepository) UsageCount(ctx context.Context, id int64) (int, error) {
	if r.db != nil {
		var count int
		err := r.db.QueryRowContext(ctx, `
			SELECT
				(SELECT COUNT(*) FROM users WHERE auto_level_id = $1) +
				(SELECT COUNT(*) FROM users WHERE manual_level_id = $1) +
				(SELECT COUNT(*) FROM groups WHERE required_level_id = $1)
		`, id).Scan(&count)
		return count, err
	}
	autoUsers, err := r.client.User.Query().Where(user.AutoLevelIDEQ(id)).Count(ctx)
	if err != nil {
		return 0, err
	}
	manualUsers, err := r.client.User.Query().Where(user.ManualLevelIDEQ(id)).Count(ctx)
	if err != nil {
		return 0, err
	}
	groups, err := r.client.Group.Query().Where(group.RequiredLevelIDEQ(id)).Count(ctx)
	return autoUsers + manualUsers + groups, err
}

func (r *accessLevelRepository) ReferenceIDs(ctx context.Context, id int64) ([]int64, []int64, error) {
	autoIDs, err := r.client.User.Query().Where(user.AutoLevelIDEQ(id)).IDs(ctx)
	if err != nil {
		return nil, nil, err
	}
	manualIDs, err := r.client.User.Query().Where(user.ManualLevelIDEQ(id)).IDs(ctx)
	if err != nil {
		return nil, nil, err
	}
	seen := make(map[int64]struct{}, len(autoIDs)+len(manualIDs))
	userIDs := make([]int64, 0, len(autoIDs)+len(manualIDs))
	for _, userID := range append(autoIDs, manualIDs...) {
		if _, ok := seen[userID]; ok {
			continue
		}
		seen[userID] = struct{}{}
		userIDs = append(userIDs, userID)
	}
	groupIDs, err := r.client.Group.Query().Where(group.RequiredLevelIDEQ(id)).IDs(ctx)
	return userIDs, groupIDs, err
}

func (r *accessLevelRepository) RecomputeUsers(ctx context.Context, maintenanceEnabled bool) ([]int64, error) {
	if r.db == nil {
		return nil, errors.New("database is not configured")
	}
	rows, err := r.db.QueryContext(ctx, `
		WITH targets AS (
			SELECT u.id,
				COALESCE(
					(SELECT l.id FROM access_levels l WHERE l.balance_threshold <= u.balance ORDER BY l.rank DESC, l.id ASC LIMIT 1),
					(SELECT l.id FROM access_levels l ORDER BY l.rank ASC, l.id ASC LIMIT 1)
				) AS target_id,
				COALESCE(
					(SELECT l.rank FROM access_levels l WHERE l.balance_threshold <= u.balance ORDER BY l.rank DESC, l.id ASC LIMIT 1),
					(SELECT l.rank FROM access_levels l ORDER BY l.rank ASC, l.id ASC LIMIT 1)
				) AS target_rank,
				current_level.rank AS current_rank
			FROM users u
			LEFT JOIN access_levels current_level ON current_level.id = u.auto_level_id
			WHERE u.deleted_at IS NULL
		)
		UPDATE users u
		SET auto_level_id = targets.target_id
		FROM targets
		WHERE u.id = targets.id
		  AND targets.target_id IS NOT NULL
		  AND u.auto_level_id IS DISTINCT FROM targets.target_id
		  AND ($1 OR targets.current_rank IS NULL OR targets.target_rank > targets.current_rank)
		RETURNING u.id
	`, maintenanceEnabled)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()
	var ids []int64
	for rows.Next() {
		var id int64
		if err := rows.Scan(&id); err != nil {
			return nil, err
		}
		ids = append(ids, id)
	}
	return ids, rows.Err()
}

func (r *accessLevelRepository) SetManualLevel(ctx context.Context, userID, levelID int64) error {
	_, err := r.client.User.UpdateOneID(userID).SetManualLevelID(levelID).Save(ctx)
	if dbent.IsNotFound(err) {
		return service.ErrUserNotFound
	}
	return err
}

func (r *accessLevelRepository) ClearManualLevel(ctx context.Context, userID int64) error {
	_, err := r.client.User.UpdateOneID(userID).ClearManualLevelID().Save(ctx)
	if dbent.IsNotFound(err) {
		return service.ErrUserNotFound
	}
	return err
}

func accessLevelEntityToService(row *dbent.AccessLevel) *service.AccessLevel {
	if row == nil {
		return nil
	}
	return &service.AccessLevel{
		ID: row.ID, Name: row.Name, Rank: row.Rank,
		BalanceThreshold:       row.BalanceThreshold,
		PaygDiscountMultiplier: row.PaygDiscountMultiplier,
		CreatedAt:              row.CreatedAt, UpdatedAt: row.UpdatedAt,
	}
}
