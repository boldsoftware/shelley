-- name: ListChatGPTAccounts :many
SELECT * FROM chatgpt_accounts ORDER BY rowid;

-- name: GetChatGPTAccount :one
SELECT * FROM chatgpt_accounts WHERE account_id = ?;

-- name: UpsertChatGPTAccount :exec
INSERT INTO chatgpt_accounts (account_id, account)
VALUES (?, ?)
ON CONFLICT (account_id) DO UPDATE SET
    account = excluded.account,
    updated_at = CURRENT_TIMESTAMP;

-- name: DeleteChatGPTAccount :execrows
DELETE FROM chatgpt_accounts WHERE account_id = ?;
