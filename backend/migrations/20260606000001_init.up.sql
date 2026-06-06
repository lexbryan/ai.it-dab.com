-- 20260606000001_init: baseline extensions for the gateway schema.
--
-- pgcrypto provides gen_random_uuid() for UUID primary keys; citext backs
-- case-insensitive unique columns (e.g. users.email). Domain tables are added
-- by later migrations. The schema_migrations bookkeeping table is created by the
-- runner itself, not here.
CREATE EXTENSION IF NOT EXISTS pgcrypto;
CREATE EXTENSION IF NOT EXISTS citext;
