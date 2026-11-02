-- name: ListMigrationNames :many
SELECT migration_name FROM migrations;

-- name: RecordMigration :exec
INSERT INTO migrations (migration_number, migration_name) VALUES (?, ?);
