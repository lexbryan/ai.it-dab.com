-- Roll back the baseline. down migrations run newest-first, so any later
-- migration that depended on these extensions has already been reverted.
DROP EXTENSION IF EXISTS citext;
DROP EXTENSION IF EXISTS pgcrypto;
