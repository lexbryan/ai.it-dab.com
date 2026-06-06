import { fireEvent, render, screen, waitFor } from '@testing-library/react';
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest';

import { getSession } from '@/lib/session';

// Hoisted so the next/navigation mock factory can close over it.
const { pushMock } = vi.hoisted(() => ({ pushMock: vi.fn() }));
vi.mock('next/navigation', () => ({
  useRouter: () => ({ push: pushMock }),
}));

import LoginPage from './page';

const fetchMock = vi.fn<typeof fetch>();

function jsonResponse(body: unknown, status: number): Response {
  return new Response(JSON.stringify(body), {
    status,
    headers: { 'content-type': 'application/json' },
  });
}

function fillAndSubmit(email: string, password: string) {
  fireEvent.change(screen.getByLabelText(/email/i), {
    target: { value: email },
  });
  fireEvent.change(screen.getByLabelText(/password/i), {
    target: { value: password },
  });
  fireEvent.click(screen.getByRole('button', { name: /^sign in$/i }));
}

describe('LoginPage', () => {
  beforeEach(() => {
    pushMock.mockReset();
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

  it('requires both fields before calling the API', () => {
    render(<LoginPage />);
    fireEvent.click(screen.getByRole('button', { name: /sign in/i }));

    expect(screen.getByRole('alert')).toHaveTextContent(/required/i);
    expect(fetchMock).not.toHaveBeenCalled();
    expect(pushMock).not.toHaveBeenCalled();
  });

  it('authenticates, persists the session, and redirects on success', async () => {
    fetchMock.mockResolvedValue(
      jsonResponse({ email: 'admin@dab.test', is_superuser: true }, 200),
    );
    render(<LoginPage />);
    fillAndSubmit('admin@dab.test', 'hunter2');

    await waitFor(() => expect(pushMock).toHaveBeenCalledWith('/dashboard'));

    const [url, init] = fetchMock.mock.calls[0];
    expect(url).toBe('http://api.test/api/admin/login');
    expect(init?.method).toBe('POST');
    expect(init?.credentials).toBe('include');
    expect(init?.body).toBe(
      JSON.stringify({ email: 'admin@dab.test', password: 'hunter2' }),
    );
    expect(getSession()).toEqual({
      email: 'admin@dab.test',
      isSuperuser: true,
    });
  });

  it('shows a generic error and stores no session on 401', async () => {
    fetchMock.mockResolvedValue(
      jsonResponse(
        {
          error: { type: 'unauthorized', message: 'invalid email or password' },
        },
        401,
      ),
    );
    render(<LoginPage />);
    fillAndSubmit('admin@dab.test', 'wrong-password');

    await waitFor(() =>
      expect(screen.getByRole('alert')).toHaveTextContent(
        /invalid email or password/i,
      ),
    );
    expect(pushMock).not.toHaveBeenCalled();
    expect(getSession()).toBeNull();
  });

  it('shows a friendly rate-limit message on 429', async () => {
    fetchMock.mockResolvedValue(
      jsonResponse(
        { error: { type: 'rate_limited', message: 'slow down' } },
        429,
      ),
    );
    render(<LoginPage />);
    fillAndSubmit('admin@dab.test', 'hunter2');

    await waitFor(() =>
      expect(screen.getByRole('alert')).toHaveTextContent(/too many attempts/i),
    );
    expect(pushMock).not.toHaveBeenCalled();
  });

  it('shows a generic fallback message and stores no session on a network error', async () => {
    fetchMock.mockRejectedValue(new TypeError('Failed to fetch'));
    render(<LoginPage />);
    fillAndSubmit('admin@dab.test', 'hunter2');

    await waitFor(() =>
      expect(screen.getByRole('alert')).toHaveTextContent(
        /something went wrong/i,
      ),
    );
    expect(pushMock).not.toHaveBeenCalled();
    expect(getSession()).toBeNull();
  });

  it('disables the submit button while the request is in flight', async () => {
    let resolveFetch: (r: Response) => void = () => {};
    fetchMock.mockReturnValue(
      new Promise<Response>((resolve) => {
        resolveFetch = resolve;
      }),
    );
    render(<LoginPage />);
    fillAndSubmit('admin@dab.test', 'hunter2');

    const button = screen.getByRole('button', { name: /signing in/i });
    expect(button).toBeDisabled();

    resolveFetch(jsonResponse({ email: 'a@b.c', is_superuser: false }, 200));
    await waitFor(() => expect(pushMock).toHaveBeenCalled());
  });
});
