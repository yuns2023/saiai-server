//go:build integration

package repository

import (
	"context"
	"testing"

	"github.com/Wei-Shaw/sub2api/migrations"
	"github.com/stretchr/testify/require"
)

func TestMigration116ClientDevicesAppliesTwice(t *testing.T) {
	ctx := context.Background()
	tx, err := integrationDB.BeginTx(ctx, nil)
	require.NoError(t, err)
	defer func() { _ = tx.Rollback() }()
	_, err = tx.ExecContext(ctx, `CREATE SCHEMA client_proof_migration_test; SET LOCAL search_path=client_proof_migration_test;
 CREATE TABLE users(id BIGINT PRIMARY KEY); CREATE TABLE api_keys(id BIGINT PRIMARY KEY);
 CREATE TABLE groups(id BIGINT PRIMARY KEY); INSERT INTO groups VALUES(1);
 CREATE TABLE usage_logs(id BIGINT PRIMARY KEY); INSERT INTO usage_logs VALUES(1);`)
	require.NoError(t, err)
	migration, err := migrations.FS.ReadFile("116_client_devices_and_attestation.sql")
	require.NoError(t, err)
	for i := 0; i < 2; i++ {
		_, err = tx.ExecContext(ctx, string(migration))
		require.NoError(t, err)
	}
	var policy string
	require.NoError(t, tx.QueryRowContext(ctx, `SELECT client_attestation_policy FROM groups WHERE id=1`).Scan(&policy))
	require.Equal(t, "off", policy)
	var attestation *int16
	require.NoError(t, tx.QueryRowContext(ctx, `SELECT client_attestation FROM usage_logs WHERE id=1`).Scan(&attestation))
	require.Nil(t, attestation, "no backfill")
	_, err = tx.ExecContext(ctx, `UPDATE groups SET client_attestation_policy='observe' WHERE id=1`)
	require.NoError(t, err)
	_, err = tx.ExecContext(ctx, `SAVEPOINT invalid_policy`)
	require.NoError(t, err)
	_, err = tx.ExecContext(ctx, `UPDATE groups SET client_attestation_policy='strict' WHERE id=1`)
	require.Error(t, err, "CHECK constraint rejects unknown policies")
	_, err = tx.ExecContext(ctx, `ROLLBACK TO SAVEPOINT invalid_policy`)
	require.NoError(t, err)
}
