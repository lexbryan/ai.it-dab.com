import { fireEvent, render, screen, waitFor } from '@testing-library/react';
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest';

const { routerMock } = vi.hoisted(() => ({
  routerMock: { replace: vi.fn(), push: vi.fn() },
}));
vi.mock('next/navigation', () => ({
  useRouter: () => routerMock,
  useParams: () => ({ id: '1' }),
}));

import type { ApiKeyMetadata } from '@/lib/keys';
import EditKeyPage from './page';

const fetchMock = vi.fn<typeof fetch>();

function jsonResponse(body: unknown, status = 200): Response {
  return new Response(JSON.stringify(body), {
    status,
    headers: { 'content-type': 'application/json' },
  });
}

const existing: ApiKeyMetadata = {
  id: '1',
  key_id: 'dab_pk_abc',
  name: 'Prod',
  persona: 'Original persona.',
  created_at: '2026-01-01T00:00:00Z',
  revoked_at: null,
  last_used_at: null,
};

describe('EditKeyPage', () => {
  beforeEach(() => {
    fetchMock.mockReset();
    routerMock.push.mockReset();
    routerMock.replace.mockReset();
    vi.stubGlobal('fetch', fetchMock);
    vi.stubEnv('NEXT_PUBLIC_API_BASE_URL', 'http://api.test');
    window.localStorage.clear();
  });
  afterEach(() => {
    vi.unstubAllGlobals();
    vi.unstubAllEnvs();
    window.localStorage.clear();
  });

  it('loads and prefills the current name and persona', async () => {
    fetchMock.mockResolvedValue(jsonResponse({ keys: [existing] }));
    render(<EditKeyPage />);

    expect(await screen.findByDisplayValue('Prod')).toBeInTheDocument();
    expect(screen.getByDisplayValue('Original persona.')).toBeInTheDocument();
    expect(screen.getByText('dab_pk_abc')).toBeInTheDocument();
  });

  it('PATCHes the edited name/persona and returns to the list', async () => {
    fetchMock.mockResolvedValueOnce(jsonResponse({ keys: [existing] })); // load
    render(<EditKeyPage />);
    await screen.findByDisplayValue('Prod');

    fireEvent.change(screen.getByLabelText(/name/i), {
      target: { value: 'Renamed' },
    });
    fireEvent.change(screen.getByLabelText(/persona/i), {
      target: { value: 'New persona.' },
    });

    fetchMock.mockResolvedValueOnce(
      jsonResponse({ ...existing, name: 'Renamed', persona: 'New persona.' }),
    );
    fireEvent.click(screen.getByRole('button', { name: /save changes/i }));

    await waitFor(() =>
      expect(routerMock.push).toHaveBeenCalledWith('/dashboard/keys'),
    );
    const patch = fetchMock.mock.calls.find(
      (c) => (c[1] as RequestInit | undefined)?.method === 'PATCH',
    );
    expect(patch?.[0]).toBe('http://api.test/api/admin/keys/1');
    expect((patch?.[1] as RequestInit).body).toBe(
      JSON.stringify({ name: 'Renamed', persona: 'New persona.' }),
    );
  });

  it('shows a not-found message when the key is gone', async () => {
    fetchMock.mockResolvedValue(jsonResponse({ keys: [] }));
    render(<EditKeyPage />);
    expect(await screen.findByText(/no longer exists/i)).toBeInTheDocument();
  });

  it('requires a non-empty name', async () => {
    fetchMock.mockResolvedValue(jsonResponse({ keys: [existing] }));
    render(<EditKeyPage />);
    await screen.findByDisplayValue('Prod');

    fireEvent.change(screen.getByLabelText(/name/i), {
      target: { value: '  ' },
    });
    fireEvent.click(screen.getByRole('button', { name: /save changes/i }));

    expect(screen.getByRole('alert')).toHaveTextContent(/name is required/i);
    const patch = fetchMock.mock.calls.find(
      (c) => (c[1] as RequestInit | undefined)?.method === 'PATCH',
    );
    expect(patch).toBeUndefined();
  });

  it('redirects to /login when the load returns 401', async () => {
    fetchMock.mockResolvedValue(
      jsonResponse({ error: { type: 'unauthorized', message: 'nope' } }, 401),
    );
    render(<EditKeyPage />);
    await waitFor(() =>
      expect(routerMock.replace).toHaveBeenCalledWith('/login'),
    );
  });
});
