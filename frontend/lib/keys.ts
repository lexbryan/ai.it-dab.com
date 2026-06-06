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

/** Fetch every API credential (active and revoked) as non-secret metadata. */
export async function listKeys(
  options?: ApiRequestOptions,
): Promise<ApiKeyMetadata[]> {
  const res = await api.get<ListKeysResponse>('/api/admin/keys', options);
  return res.keys;
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
