-- 20260606000002_create_users: admin / superuser accounts.
CREATE TABLE users (
	id            uuid PRIMARY KEY DEFAULT gen_random_uuid(),
	email         citext NOT NULL,
	password_hash text NOT NULL,
	is_superuser  boolean NOT NULL DEFAULT false,
	created_at    timestamptz NOT NULL DEFAULT now(),
	updated_at    timestamptz NOT NULL DEFAULT now()
);

-- citext makes the column compare case-insensitively, so this unique index
-- enforces that emails differing only by case collide at the database level.
CREATE UNIQUE INDEX users_email_key ON users (email);
