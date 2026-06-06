'use client';

import { useRouter } from 'next/navigation';
import { useCallback, useEffect, useState } from 'react';

import { ApiError } from './api-client';
import { logout as performLogout } from './auth';
import { clearSession, getSession, type AdminSession } from './session';

/**
 * Client-side route guard. The session identity lives in localStorage and the
 * JWT in an HttpOnly cookie on the (cross-origin) gateway, so neither is
 * readable by server middleware on this origin — the guard must run on the
 * client. It redirects to /login when there is no session and returns the
 * session once verified (null while checking or redirecting).
 */
export function useRequireAuth(): AdminSession | null {
  const router = useRouter();
  const [session, setSessionState] = useState<AdminSession | null>(null);

  useEffect(() => {
    const current = getSession();
    if (current === null) {
      router.replace('/login');
      return;
    }
    setSessionState(current);
  }, [router]);

  return session;
}

/**
 * Returns a logout handler: clears the session (plus a best-effort backend
 * cookie clear) and redirects to /login.
 */
export function useLogout(): () => Promise<void> {
  const router = useRouter();
  return useCallback(async () => {
    await performLogout();
    router.replace('/login');
  }, [router]);
}

/**
 * Returns a handler for failed authenticated requests: when the error is a 401
 * (expired/invalid session) it clears the session, redirects to /login, and
 * returns true; otherwise it returns false and leaves the session intact. Pages
 * that call protected endpoints route their errors through this.
 */
export function useAuthErrorRedirect(): (err: unknown) => boolean {
  const router = useRouter();
  return useCallback(
    (err: unknown): boolean => {
      if (err instanceof ApiError && err.status === 401) {
        clearSession();
        router.replace('/login');
        return true;
      }
      return false;
    },
    [router],
  );
}
