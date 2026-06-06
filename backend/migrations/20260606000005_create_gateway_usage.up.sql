-- 20260606000005_create_gateway_usage: a durable, content-free audit row per
-- gateway LLM call. Exactly one row is written per call (success or failure) for
-- usage accounting and observability. It stores counts, an outcome, and timings
-- ONLY — never message content, project secrets, the VLLM_API_KEY, or upstream
-- bodies. api_key_id cascades with the credential; conversation_id is nullable
-- and survives a conversation deletion so the audit trail is not lost.
CREATE TABLE gateway_usage (
	id                     uuid PRIMARY KEY DEFAULT gen_random_uuid(),
	api_key_id             uuid NOT NULL REFERENCES api_keys (id) ON DELETE CASCADE,
	conversation_id        uuid REFERENCES conversations (id) ON DELETE SET NULL,
	model                  text NOT NULL,
	stream                 boolean NOT NULL,
	prompt_msg_count       integer NOT NULL,
	prompt_token_count     integer,
	completion_token_count integer,
	upstream_status        integer,
	outcome                text NOT NULL,
	latency_ms             integer NOT NULL,
	created_at             timestamptz NOT NULL DEFAULT now()
);

-- Per-credential usage queries, most-recent first.
CREATE INDEX gateway_usage_api_key_created_idx ON gateway_usage (api_key_id, created_at);
