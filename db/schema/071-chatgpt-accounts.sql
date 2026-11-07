-- Sign in with ChatGPT accounts. account is the siwc.Account as JSON: the
-- registration's issued client ID, the verified identity, and the current
-- tokens, which a refresh replaces together. Rows keep registration order.
CREATE TABLE chatgpt_accounts (
    account_id TEXT PRIMARY KEY,
    account TEXT NOT NULL,
    updated_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP
);
