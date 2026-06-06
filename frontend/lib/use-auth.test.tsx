import { renderHook, waitFor } from '@testing-library/react';
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest';

// A STABLE router object: next/navigation's useRouter returns a stable
// reference, and hooks depend on it in effect deps, so the mock must too (a
// fresh object each call would loop effects).
const { replaceMock, routerMock } = vi.hoisted(() => {
  const replace = vi.fn();
  return { replaceMock: replace, routerMock: { replace, push: vi.fn() } };
});
vi.mock('next/navigation', () => ({
  useRouter: () => routerMock,
}));

import { ApiError } from './api-client';
import { getSession, setSession } from './session';
import { useAuthErrorRedirect, useLogout, useRequireAuth } from './use-auth';

const fetchMock = vi.fn<typeof fetch>();

beforeEach(() => {
  replaceMock.mockReset();
  fetchMock.mockReset();
  vi.stubGlobal('fetch', fetchMock);
  vi.stubEnv('NEXT_PUBLIC_API_BASE_URL', 'http://api.test');
  window.localStorage.clear();
});

afterEach(() => {
  vi.unstubAllGlobals();
  vi.unstubAllEnvs();
  window.localStorage.clear();
});

describe('useRequireAuth', () => {
  it('redirects to /login when there is no session', async () => {
    const { result } = renderHook(() => useRequireAuth());
    await waitFor(() => expect(replaceMock).toHaveBeenCalledWith('/login'));
    expect(result.current).toBeNull();
  });

  it('returns the session and does not redirect when authenticated', async () => {
    setSession({ email: 'admin@dab.test', isSuperuser: false });
    const { result } = renderHook(() => useRequireAuth());
    await waitFor(() =>
      expect(result.current).toEqual({
        email: 'admin@dab.test',
        isSuperuser: false,
      }),
    );
    expect(replaceMock).not.toHaveBeenCalled();
  });
});

describe('useLogout', () => {
  it('clears the session, calls the backend logout, and redirects', async () => {
    setSession({ email: 'admin@dab.test', isSuperuser: true });
    fetchMock.mockResolvedValue(new Response(null, { status: 204 }));

    const { result } = renderHook(() => useLogout());
    await result.current();

    expect(getSession()).toBeNull();
    expect(fetchMock.mock.calls[0][0]).toBe('http://api.test/api/admin/logout');
    expect(replaceMock).toHaveBeenCalledWith('/login');
  });

  it('still clears and redirects if the backend logout fails', async () => {
    setSession({ email: 'admin@dab.test', isSuperuser: true });
    fetchMock.mockRejectedValue(new TypeError('network down'));

    const { result } = renderHook(() => useLogout());
    await result.current();

    expect(getSession()).toBeNull();
    expect(replaceMock).toHaveBeenCalledWith('/login');
  });
});

describe('useAuthErrorRedirect', () => {
  it('clears the session and redirects on a 401', () => {
    setSession({ email: 'admin@dab.test', isSuperuser: false });
    const { result } = renderHook(() => useAuthErrorRedirect());

    const handled = result.current(new ApiError('unauthorized', 401));

    expect(handled).toBe(true);
    expect(getSession()).toBeNull();
    expect(replaceMock).toHaveBeenCalledWith('/login');
  });

  it('ignores non-401 errors and leaves the session intact', () => {
    setSession({ email: 'admin@dab.test', isSuperuser: false });
    const { result } = renderHook(() => useAuthErrorRedirect());

    const handled = result.current(new ApiError('server error', 500));

    expect(handled).toBe(false);
    expect(getSession()).not.toBeNull();
    expect(replaceMock).not.toHaveBeenCalled();
  });
});
