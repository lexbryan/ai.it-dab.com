/**
 * Admin session module — the single source of truth for the admin UI's auth
 * state.
 *
 * The session JWT lives in an HttpOnly cookie set by the backend, so JavaScript
 * can neither read nor forge it. This module persists only the non-sensitive
 * user identity the login endpoint returns ({ email, isSuperuser }) so the UI
 * can render the signed-in state across reloads. Authorization itself is always
 * enforced server-side: an authenticated request whose cookie is missing or
 * expired comes back 401, at which point callers clear the session.
 */

export interface AdminSession {
  email: string;
  isSuperuser: boolean;
}

const STORAGE_KEY = 'dab.admin.session';

/** Access localStorage defensively (absent during SSR; can throw if disabled). */
function storage(): Storage | null {
  try {
    return typeof window === 'undefined' ? null : window.localStorage;
  } catch {
    return null;
  }
}

/** Returns the persisted admin session, or null when signed out. */
export function getSession(): AdminSession | null {
  const store = storage();
  if (!store) return null;

  const raw = store.getItem(STORAGE_KEY);
  if (!raw) return null;

  try {
    const parsed: unknown = JSON.parse(raw);
    if (
      parsed !== null &&
      typeof parsed === 'object' &&
      typeof (parsed as AdminSession).email === 'string' &&
      typeof (parsed as AdminSession).isSuperuser === 'boolean'
    ) {
      const { email, isSuperuser } = parsed as AdminSession;
      return { email, isSuperuser };
    }
  } catch {
    // malformed JSON — fall through and clear it below
  }
  store.removeItem(STORAGE_KEY);
  return null;
}

/** Persists the signed-in user identity (never the token). */
export function setSession(session: AdminSession): void {
  const store = storage();
  if (!store) return;
  store.setItem(
    STORAGE_KEY,
    JSON.stringify({ email: session.email, isSuperuser: session.isSuperuser }),
  );
}

/** Clears the persisted session (sign-out, or after a 401). */
export function clearSession(): void {
  const store = storage();
  if (!store) return;
  store.removeItem(STORAGE_KEY);
}

/** True when a session is persisted. */
export function isAuthenticated(): boolean {
  return getSession() !== null;
}
