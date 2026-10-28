package repository

import (
	"context"
	"time"

	"github.com/Wei-Shaw/sub2api/ent"
	"github.com/Wei-Shaw/sub2api/ent/setting"
	"github.com/Wei-Shaw/sub2api/internal/service"
)

type settingRepository struct {
	client *ent.Client
}

func NewSettingRepository(client *ent.Client) service.SettingRepository {
	return &settingRepository{client: client}
}

func (r *settingRepository) Get(ctx context.Context, key string) (*service.Setting, error) {
	m, err := r.client.Setting.Query().Where(setting.KeyEQ(key)).Only(ctx)
	if err != nil {
		if ent.IsNotFound(err) {
			return nil, service.ErrSettingNotFound
		}
		return nil, err
	}
	return &service.Setting{
		ID:        m.ID,
		Key:       m.Key,
		Value:     m.Value,
		UpdatedAt: m.UpdatedAt,
	}, nil
}

func (r *settingRepository) GetValue(ctx context.Context, key string) (string, error) {
	setting, err := r.Get(ctx, key)
	if err != nil {
		return "", err
	}
	return setting.Value, nil
}

func (r *settingRepository) Set(ctx context.Context, key, value string) error {
	now := time.Now()
	return r.client.Setting.
		Create().
		SetKey(key).
		SetValue(value).
		SetUpdatedAt(now).
		OnConflictColumns(setting.FieldKey).
		UpdateNewValues().
		Exec(ctx)
}

func (r *settingRepository) GetMultiple(ctx context.Context, keys []string) (map[string]string, error) {
	if len(keys) == 0 {
		return map[string]string{}, nil
	}
	settings, err := r.client.Setting.Query().Where(setting.KeyIn(keys...)).All(ctx)
	if err != nil {
		return nil, err
	}

	result := make(map[string]string)
	for _, s := range settings {
		result[s.Key] = s.Value
	}
	return result, nil
}

func (r *settingRepository) SetMultiple(ctx context.Context, settings map[string]string) error {
	if len(settings) == 0 {
		return nil
	}

	now := time.Now()
	builders := make([]*ent.SettingCreate, 0, len(settings))
	for key, value := range settings {
		builders = append(builders, r.client.Setting.Create().SetKey(key).SetValue(value).SetUpdatedAt(now))
	}
	return r.client.Setting.
		CreateBulk(builders...).
		OnConflictColumns(setting.FieldKey).
		UpdateNewValues().
		Exec(ctx)
}

func (r *settingRepository) GetAll(ctx context.Context) (map[string]string, error) {
	settings, err := r.client.Setting.Query().All(ctx)
	if err != nil {
		return nil, err
	}

	result := make(map[string]string)
	for _, s := range settings {
		result[s.Key] = s.Value
	}
	return result, nil
}

func (r *settingRepository) Delete(ctx context.Context, key string) error {
	_, err := r.client.Setting.Delete().Where(setting.KeyEQ(key)).Exec(ctx)
	return err
}

// UpdateSettingValue replaces one value under a row lock, so concurrent
// read-modify-write callers (Gateways, administrators) never lose an update.
// A first insert racing another one retries once with a fresh read.
func (r *settingRepository) UpdateSettingValue(ctx context.Context, key string, update func(current string) (string, error)) error {
	err := r.updateSettingValueOnce(ctx, key, update)
	if isUniqueConstraintViolation(err) {
		err = r.updateSettingValueOnce(ctx, key, update)
	}
	return err
}

func (r *settingRepository) updateSettingValueOnce(ctx context.Context, key string, update func(current string) (string, error)) error {
	// Requires a non-transactional client (ent.ErrTxStarted otherwise): the
	// caller relies on the value being committed when this returns.
	tx, err := r.client.Tx(ctx)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()
	if err := updateSettingValueLocked(ctx, tx.Client(), key, update); err != nil {
		return err
	}
	return tx.Commit()
}

func updateSettingValueLocked(ctx context.Context, client *ent.Client, key string, update func(current string) (string, error)) error {
	current, err := client.Setting.Query().Where(setting.KeyEQ(key)).ForUpdate().Only(ctx)
	if err != nil && !ent.IsNotFound(err) {
		return err
	}
	value := ""
	if current != nil {
		value = current.Value
	}
	next, err := update(value)
	if err != nil {
		return err
	}
	now := time.Now()
	if current == nil {
		return client.Setting.Create().SetKey(key).SetValue(next).SetUpdatedAt(now).Exec(ctx)
	}
	if next == value {
		return nil
	}
	return client.Setting.UpdateOne(current).SetValue(next).SetUpdatedAt(now).Exec(ctx)
}
