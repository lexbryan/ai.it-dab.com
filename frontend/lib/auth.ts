import { api, type ApiRequestOptions } from './api-client';
import { type AdminSession, clearSession, setSession } from './session';

/** Backend admin auth endpoints (see backend internal/admin). */
export const LOGIN_PATH = '/api/admin/login';
export const LOGOUT_PATH = '/api/admin/logout';

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

/**
 * Sign out. Always clears the local session; additionally makes a best-effort
 * call to the backend logout endpoint to clear the HttpOnly session cookie.
 *
 * The local clear is what gates the UI, so it happens even if the backend call
 * fails or the endpoint is not yet available — the cookie also expires on its
 * own. (A dedicated backend logout endpoint is a small follow-up; until then the
 * call is a no-op the browser ignores.)
 */
export async function logout(options?: ApiRequestOptions): Promise<void> {
  try {
    await api.post(LOGOUT_PATH, undefined, options);
  } catch {
    // best-effort — see the doc comment above
  }
  clearSession();
}
