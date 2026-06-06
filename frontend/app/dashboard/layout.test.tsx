import { fireEvent, render, screen, waitFor } from '@testing-library/react';
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest';

// Stable router object — useRequireAuth depends on the router in an effect, so a
// fresh object per render would loop. next/navigation's useRouter is stable too.
const { replaceMock, routerMock } = vi.hoisted(() => {
  const replace = vi.fn();
  return { replaceMock: replace, routerMock: { replace, push: vi.fn() } };
});
vi.mock('next/navigation', () => ({
  useRouter: () => routerMock,
}));

import { getSession, setSession } from '@/lib/session';
import DashboardLayout from './layout';

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

describe('DashboardLayout', () => {
  it('redirects unauthenticated users to /login and hides protected content', async () => {
    render(
      <DashboardLayout>
        <div>secret-content</div>
      </DashboardLayout>,
    );

    await waitFor(() => expect(replaceMock).toHaveBeenCalledWith('/login'));
    expect(screen.queryByText('secret-content')).not.toBeInTheDocument();
  });

  it('renders the shell with the user and protected content when authenticated', async () => {
    setSession({ email: 'admin@dab.test', isSuperuser: true });
    render(
      <DashboardLayout>
        <div>secret-content</div>
      </DashboardLayout>,
    );

    expect(await screen.findByText('secret-content')).toBeInTheDocument();
    expect(screen.getByText('admin@dab.test')).toBeInTheDocument();
    expect(
      screen.getByRole('button', { name: /log out/i }),
    ).toBeInTheDocument();
    expect(replaceMock).not.toHaveBeenCalled();
  });

  it('logs out: clears the session, calls the backend, and redirects to /login', async () => {
    setSession({ email: 'admin@dab.test', isSuperuser: true });
    fetchMock.mockResolvedValue(new Response(null, { status: 204 }));
    render(
      <DashboardLayout>
        <div>secret-content</div>
      </DashboardLayout>,
    );

    fireEvent.click(await screen.findByRole('button', { name: /log out/i }));

    await waitFor(() => expect(getSession()).toBeNull());
    expect(fetchMock.mock.calls[0][0]).toBe('http://api.test/api/admin/logout');
    await waitFor(() => expect(replaceMock).toHaveBeenCalledWith('/login'));
  });
});
