/**
 * Shared API client: the single place the admin UI talks to the gateway.
 *
 * The base URL is injected at build time via NEXT_PUBLIC_API_BASE_URL (see the
 * root .env.example). Every request sends `credentials: 'include'` so the
 * admin's cookie-borne session rides along, defaults to JSON, and normalizes any
 * transport failure or non-2xx response into a typed {@link ApiError}.
 */

export type QueryValue = string | number | boolean | null | undefined;

export interface ApiRequestOptions extends Omit<RequestInit, 'body'> {
  /** JSON-serializable request body; sets `Content-Type: application/json`. */
  body?: unknown;
  /** Query params appended to the path; null/undefined values are skipped. */
  query?: Record<string, QueryValue>;
  /** Override the resolved base URL (tests, server-side calls). */
  baseUrl?: string;
}

/** Error thrown for network failures (status 0) and non-2xx responses. */
export class ApiError extends Error {
  readonly status: number;
  readonly data: unknown;

  constructor(message: string, status: number, data: unknown = null) {
    super(message);
    this.name = 'ApiError';
    this.status = status;
    this.data = data;
  }

  /** True when the failure was a transport error (no HTTP response arrived). */
  get isNetworkError(): boolean {
    return this.status === 0;
  }
}

/** Resolve the gateway base URL, trimming any trailing slash. */
export function resolveBaseUrl(override?: string): string {
  const base = override ?? process.env.NEXT_PUBLIC_API_BASE_URL ?? '';
  return base.replace(/\/+$/, '');
}

function buildUrl(
  baseUrl: string,
  path: string,
  query?: Record<string, QueryValue>,
): string {
  const normalizedPath = path.startsWith('/') ? path : `/${path}`;
  let url = `${baseUrl}${normalizedPath}`;
  if (query) {
    const params = new URLSearchParams();
    for (const [key, value] of Object.entries(query)) {
      if (value !== undefined && value !== null) {
        params.append(key, String(value));
      }
    }
    const qs = params.toString();
    if (qs) {
      url += `${url.includes('?') ? '&' : '?'}${qs}`;
    }
  }
  return url;
}

async function parseResponseBody(response: Response): Promise<unknown> {
  if (response.status === 204 || response.status === 205) {
    return null;
  }
  const contentType = response.headers.get('content-type') ?? '';
  if (contentType.includes('application/json')) {
    try {
      return await response.json();
    } catch {
      return null;
    }
  }
  const text = await response.text();
  return text === '' ? null : text;
}

function messageFromBody(body: unknown, fallback: string): string {
  if (body && typeof body === 'object') {
    const record = body as Record<string, unknown>;
    for (const key of ['error', 'message', 'detail']) {
      const value = record[key];
      if (typeof value === 'string' && value !== '') {
        return value;
      }
    }
  }
  if (typeof body === 'string' && body !== '') {
    return body;
  }
  return fallback;
}

/**
 * Perform a JSON request against the gateway and return the parsed body typed as
 * T. Throws {@link ApiError} on transport failure or any non-2xx response.
 */
export async function apiFetch<T = unknown>(
  path: string,
  options: ApiRequestOptions = {},
): Promise<T> {
  const { body, query, baseUrl, headers, ...rest } = options;
  const url = buildUrl(resolveBaseUrl(baseUrl), path, query);

  const finalHeaders = new Headers(headers);
  if (!finalHeaders.has('Accept')) {
    finalHeaders.set('Accept', 'application/json');
  }

  const init: RequestInit = {
    ...rest,
    headers: finalHeaders,
    // Always send the session cookie cross-origin; pairs with the gateway's
    // credentialed CORS policy.
    credentials: 'include',
  };
  if (body !== undefined) {
    if (!finalHeaders.has('Content-Type')) {
      finalHeaders.set('Content-Type', 'application/json');
    }
    init.body = JSON.stringify(body);
  }

  let response: Response;
  try {
    response = await fetch(url, init);
  } catch (cause) {
    throw new ApiError('network request failed', 0, cause);
  }

  const data = await parseResponseBody(response);
  if (!response.ok) {
    throw new ApiError(
      messageFromBody(data, `request failed with status ${response.status}`),
      response.status,
      data,
    );
  }
  return data as T;
}

/** Convenience verb helpers over {@link apiFetch}. */
export const api = {
  get: <T = unknown>(path: string, options?: ApiRequestOptions) =>
    apiFetch<T>(path, { ...options, method: 'GET' }),
  post: <T = unknown>(
    path: string,
    body?: unknown,
    options?: ApiRequestOptions,
  ) => apiFetch<T>(path, { ...options, method: 'POST', body }),
  put: <T = unknown>(
    path: string,
    body?: unknown,
    options?: ApiRequestOptions,
  ) => apiFetch<T>(path, { ...options, method: 'PUT', body }),
  patch: <T = unknown>(
    path: string,
    body?: unknown,
    options?: ApiRequestOptions,
  ) => apiFetch<T>(path, { ...options, method: 'PATCH', body }),
  delete: <T = unknown>(path: string, options?: ApiRequestOptions) =>
    apiFetch<T>(path, { ...options, method: 'DELETE' }),
};
