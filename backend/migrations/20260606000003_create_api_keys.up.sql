-- 20260606000003_create_api_keys: two-key + persona API credentials.
--
-- A credential is a PAIR: a public key id (dab_pk_..., stored plainly in key_id)
-- and a secret (dab_sk_...) stored ONLY as a hash in secret_hash. The plaintext
-- secret is never persisted. persona is a per-credential system prompt (plain
-- text, NOT a secret) the gateway injects as the leading system message on every
-- LLM call, so even a fine-tuned model receives the project's persona.
CREATE TABLE api_keys (
	id           uuid PRIMARY KEY DEFAULT gen_random_uuid(),
	key_id       text NOT NULL,
	secret_hash  text NOT NULL,
	name         text NOT NULL DEFAULT '',
	persona      text,
	created_by   uuid REFERENCES users (id) ON DELETE SET NULL,
	created_at   timestamptz NOT NULL DEFAULT now(),
	revoked_at   timestamptz,
	last_used_at timestamptz
);

-- key_id is the public half presented on every request. It is globally unique
-- and never reused, even after revocation, so one unique index both enforces
-- uniqueness and serves the active-credential lookup
-- (WHERE key_id = $1 AND revoked_at IS NULL). A separate partial "active" index
-- would be redundant given key_id already maps to at most one row.
CREATE UNIQUE INDEX api_keys_key_id_key ON api_keys (key_id);

-- created_by references the admin (users row) who created the credential, for
-- ownership/audit. It is nullable so system/bootstrap-created keys need no
-- creator, and ON DELETE SET NULL keeps a live credential working if the
-- creating admin is later removed (deleting an admin must not silently kill a
-- project's API access). The project a credential serves is identified by its
-- human-readable name, since this system has no separate projects table.
