import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest';

import { isActive, listKeys, revokeKey, type ApiKeyMetadata } from './keys';

const fetchMock = vi.fn<typeof fetch>();

function jsonResponse(body: unknown, status = 200): Response {
  return new Response(JSON.stringify(body), {
    status,
    headers: { 'content-type': 'application/json' },
  });
}

const sampleKey: ApiKeyMetadata = {
  id: 'k1',
  key_id: 'dab_pk_abc',
  name: 'Prod',
  persona: null,
  created_at: '2026-01-01T00:00:00Z',
  revoked_at: null,
  last_used_at: null,
};

describe('keys api', () => {
  beforeEach(() => {
    fetchMock.mockReset();
    vi.stubGlobal('fetch', fetchMock);
    vi.stubEnv('NEXT_PUBLIC_API_BASE_URL', 'http://api.test');
  });
  afterEach(() => {
    vi.unstubAllGlobals();
    vi.unstubAllEnvs();
  });

  it('listKeys GETs the admin keys endpoint and returns the array', async () => {
    fetchMock.mockResolvedValue(jsonResponse({ keys: [sampleKey] }));

    const keys = await listKeys();

    expect(fetchMock.mock.calls[0][0]).toBe('http://api.test/api/admin/keys');
    expect(fetchMock.mock.calls[0][1]?.method).toBe('GET');
    expect(fetchMock.mock.calls[0][1]?.credentials).toBe('include');
    expect(keys).toEqual([sampleKey]);
  });

  it('revokeKey DELETEs the credential by id', async () => {
    fetchMock.mockResolvedValue(new Response(null, { status: 204 }));

    await revokeKey('k1');

    const [url, init] = fetchMock.mock.calls[0];
    expect(url).toBe('http://api.test/api/admin/keys/k1');
    expect(init?.method).toBe('DELETE');
  });

  it('isActive reflects the revoked_at field', () => {
    expect(isActive(sampleKey)).toBe(true);
    expect(isActive({ ...sampleKey, revoked_at: '2026-02-01T00:00:00Z' })).toBe(
      false,
    );
  });
});
