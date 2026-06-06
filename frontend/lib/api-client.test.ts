import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest';

import { ApiError, api, apiFetch, resolveBaseUrl } from './api-client';

function jsonResponse(
  body: unknown,
  init: { status?: number; headers?: Record<string, string> } = {},
): Response {
  return new Response(JSON.stringify(body), {
    status: init.status ?? 200,
    headers: { 'content-type': 'application/json', ...(init.headers ?? {}) },
  });
}

/** Await a promise expected to reject with an ApiError and return it typed. */
async function captureApiError(p: Promise<unknown>): Promise<ApiError> {
  try {
    await p;
  } catch (e) {
    if (e instanceof ApiError) {
      return e;
    }
    throw e;
  }
  throw new Error('expected the request to reject with an ApiError');
}

const fetchMock = vi.fn<typeof fetch>();

describe('api client', () => {
  beforeEach(() => {
    fetchMock.mockReset();
    vi.stubGlobal('fetch', fetchMock);
    vi.stubEnv('NEXT_PUBLIC_API_BASE_URL', 'http://api.test');
  });

  afterEach(() => {
    vi.unstubAllGlobals();
    vi.unstubAllEnvs();
  });

  it('builds the URL from NEXT_PUBLIC_API_BASE_URL and sends JSON + credentials', async () => {
    fetchMock.mockResolvedValue(jsonResponse({ ok: true }));

    const result = await api.get<{ ok: boolean }>('/healthz');

    expect(result).toEqual({ ok: true });
    expect(fetchMock).toHaveBeenCalledTimes(1);
    const [url, init] = fetchMock.mock.calls[0];
    expect(url).toBe('http://api.test/healthz');
    expect(init?.method).toBe('GET');
    expect(init?.credentials).toBe('include');
    expect(new Headers(init?.headers).get('Accept')).toBe('application/json');
  });

  it('serializes a POST body and sets Content-Type', async () => {
    fetchMock.mockResolvedValue(jsonResponse({ id: 1 }, { status: 201 }));

    await api.post('/v1/keys', { name: 'svc' });

    const [, init] = fetchMock.mock.calls[0];
    expect(init?.method).toBe('POST');
    expect(init?.body).toBe(JSON.stringify({ name: 'svc' }));
    expect(new Headers(init?.headers).get('Content-Type')).toBe(
      'application/json',
    );
  });

  it('appends query params, skipping null/undefined', async () => {
    fetchMock.mockResolvedValue(jsonResponse([]));

    await api.get('/v1/keys', {
      query: { page: 2, q: 'x', skip: undefined, none: null },
    });

    expect(fetchMock.mock.calls[0][0]).toBe(
      'http://api.test/v1/keys?page=2&q=x',
    );
  });

  it('normalizes a non-2xx JSON response into a typed ApiError', async () => {
    fetchMock.mockResolvedValue(
      jsonResponse({ error: 'bad request' }, { status: 400 }),
    );

    const err = await captureApiError(api.get('/v1/keys'));

    expect(err).toBeInstanceOf(ApiError);
    expect(err.status).toBe(400);
    expect(err.message).toBe('bad request');
    expect(err.data).toEqual({ error: 'bad request' });
    expect(err.isNetworkError).toBe(false);
  });

  it('wraps a transport failure as a network ApiError (status 0)', async () => {
    fetchMock.mockRejectedValue(new TypeError('Failed to fetch'));

    const err = await captureApiError(apiFetch('/healthz'));

    expect(err.status).toBe(0);
    expect(err.isNetworkError).toBe(true);
    expect(err.message).toBe('network request failed');
  });

  it('returns null for a 204 No Content response', async () => {
    fetchMock.mockResolvedValue(new Response(null, { status: 204 }));

    const result = await api.delete('/v1/keys/abc');

    expect(result).toBeNull();
  });

  it('normalizes a non-JSON error body into the ApiError message', async () => {
    fetchMock.mockResolvedValue(
      new Response('upstream exploded', {
        status: 502,
        headers: { 'content-type': 'text/plain' },
      }),
    );

    const err = await captureApiError(api.get('/v1/keys'));

    expect(err.status).toBe(502);
    expect(err.message).toBe('upstream exploded');
    expect(err.data).toBe('upstream exploded');
  });

  it('resolveBaseUrl trims a trailing slash and honors an override', () => {
    expect(resolveBaseUrl('http://x.test/')).toBe('http://x.test');
    expect(resolveBaseUrl()).toBe('http://api.test');
  });
});
