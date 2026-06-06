import { api, type ApiRequestOptions } from './api-client';
import { type AdminSession, setSession } from './session';

/** Backend admin auth endpoint (see backend internal/admin). */
export const LOGIN_PATH = '/api/admin/login';

/** Success body returned by the login endpoint. */
interface LoginResponse {
  email: string;
  is_superuser: boolean;
}

/**
 * Authenticate against the backend admin login endpoint with email + password.
 *
 * On success the backend sets the HttpOnly session cookie and returns the user
 * identity, which is persisted via the session module. The shared API client
 * sends `credentials: 'include'`, so the cookie is stored and replayed on later
 * requests. Throws an ApiError on failure (401 bad credentials, 429 rate
 * limited, 400 validation, …) for the caller to surface.
 */
export async function login(
  email: string,
  password: string,
  options?: ApiRequestOptions,
): Promise<AdminSession> {
  const res = await api.post<LoginResponse>(
    LOGIN_PATH,
    { email, password },
    options,
  );
  const session: AdminSession = {
    email: res.email,
    isSuperuser: res.is_superuser,
  };
  setSession(session);
  return session;
}
