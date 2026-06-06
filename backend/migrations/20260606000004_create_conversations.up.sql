-- 20260606000004_create_conversations: server-side conversation context,
-- scoped per credential, with a gateway-issued session id.

-- conversations.id is the SESSION ID returned to callers. It defaults to
-- gen_random_uuid(), so the gateway issues it server-side on the first turn —
-- clients never mint session ids. Each conversation is owned by exactly one
-- api_key_id (tenant scoping); deleting the credential cascades its history.
CREATE TABLE conversations (
	id         uuid PRIMARY KEY DEFAULT gen_random_uuid(),
	api_key_id uuid NOT NULL REFERENCES api_keys (id) ON DELETE CASCADE,
	model      text,
	created_at timestamptz NOT NULL DEFAULT now(),
	updated_at timestamptz NOT NULL DEFAULT now()
);

-- (api_key_id, id) serves the scoped fetch GetConversation(api_key_id, session);
-- (api_key_id, updated_at) serves recent-first listing within a credential.
CREATE INDEX conversations_api_key_id_idx ON conversations (api_key_id, id);
CREATE INDEX conversations_api_key_updated_idx ON conversations (api_key_id, updated_at);

CREATE TABLE messages (
	id              uuid PRIMARY KEY DEFAULT gen_random_uuid(),
	conversation_id uuid NOT NULL REFERENCES conversations (id) ON DELETE CASCADE,
	role            text NOT NULL CHECK (role IN ('system', 'user', 'assistant')),
	content         text NOT NULL,
	token_count     integer,
	created_at      timestamptz NOT NULL DEFAULT now()
);

-- Ordered history loads: messages of a conversation, oldest first.
CREATE INDEX messages_conversation_created_idx ON messages (conversation_id, created_at);
