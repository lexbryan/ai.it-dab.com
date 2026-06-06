import { api, type ApiRequestOptions } from './api-client';

/**
 * Non-secret metadata for an API credential, matching the backend list/update
 * response. There is deliberately no `secret` or hash field — the plaintext
 * secret is returned only once at creation and never through these views.
 */
export interface ApiKeyMetadata {
  id: string;
  key_id: string;
  name: string;
  persona: string | null;
  created_at: string;
  revoked_at: string | null;
  last_used_at: string | null;
}

interface ListKeysResponse {
  keys: ApiKeyMetadata[];
}

/**
 * The create response. It is the ONLY place the plaintext `secret` is ever
 * returned — it cannot be retrieved again, so it must be revealed once and then
 * dropped from memory.
 */
export interface CreatedKey {
  id: string;
  key_id: string;
  secret: string;
  name: string;
  persona: string | null;
  created_at: string;
}

export interface CreateKeyInput {
  name: string;
  persona?: string | null;
}

export interface UpdateKeyInput {
  name?: string;
  persona?: string | null;
}

/** Fetch every API credential (active and revoked) as non-secret metadata. */
export async function listKeys(
  options?: ApiRequestOptions,
): Promise<ApiKeyMetadata[]> {
  const res = await api.get<ListKeysResponse>('/api/admin/keys', options);
  return res.keys;
}

/** Fetch a single credential's metadata by id, or null when absent. The backend
 * exposes no single-key GET, so this derives it from the list. */
export async function getKey(
  id: string,
  options?: ApiRequestOptions,
): Promise<ApiKeyMetadata | null> {
  const keys = await listKeys(options);
  return keys.find((key) => key.id === id) ?? null;
}

/**
 * Create a credential. The response carries the plaintext secret exactly once;
 * callers must reveal it and then discard it (never store or log it).
 */
export async function createKey(
  input: CreateKeyInput,
  options?: ApiRequestOptions,
): Promise<CreatedKey> {
  return api.post<CreatedKey>(
    '/api/admin/keys',
    { name: input.name, persona: input.persona ?? null },
    options,
  );
}

/** Update a credential's label and/or persona without rotating its secret. */
export async function updateKey(
  id: string,
  input: UpdateKeyInput,
  options?: ApiRequestOptions,
): Promise<ApiKeyMetadata> {
  return api.patch<ApiKeyMetadata>(
    `/api/admin/keys/${encodeURIComponent(id)}`,
    input,
    options,
  );
}

/** Soft-revoke a credential so it immediately stops authenticating. */
export async function revokeKey(
  id: string,
  options?: ApiRequestOptions,
): Promise<void> {
  await api.delete(`/api/admin/keys/${encodeURIComponent(id)}`, options);
}

/** True when the credential is still active (not revoked). */
export function isActive(key: ApiKeyMetadata): boolean {
  return key.revoked_at === null;
}
