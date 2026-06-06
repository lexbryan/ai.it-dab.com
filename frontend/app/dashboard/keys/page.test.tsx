import { fireEvent, render, screen, waitFor } from '@testing-library/react';
import type { ReactNode } from 'react';
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest';

// Stable router so the auth-error hook's effect deps don't loop.
const { routerMock } = vi.hoisted(() => ({
  routerMock: { replace: vi.fn(), push: vi.fn() },
}));
vi.mock('next/navigation', () => ({ useRouter: () => routerMock }));
// Render next/link as a plain anchor (no router context needed in tests).
vi.mock('next/link', () => ({
  default: ({ href, children }: { href: string; children: ReactNode }) => (
    <a href={href}>{children}</a>
  ),
}));

import type { ApiKeyMetadata } from '@/lib/keys';
import KeysPage from './page';

const fetchMock = vi.fn<typeof fetch>();

function jsonResponse(body: unknown, status = 200): Response {
  return new Response(JSON.stringify(body), {
    status,
    headers: { 'content-type': 'application/json' },
  });
}

const sampleKeys: ApiKeyMetadata[] = [
  {
    id: '1',
    key_id: 'dab_pk_abc',
    name: 'Prod',
    persona: 'You are a helpful assistant.',
    created_at: '2026-01-01T00:00:00Z',
    revoked_at: null,
    last_used_at: '2026-02-01T00:00:00Z',
  },
  {
    id: '2',
    key_id: 'dab_pk_xyz',
    name: 'Old',
    persona: null,
    created_at: '2026-01-02T00:00:00Z',
    revoked_at: '2026-03-01T00:00:00Z',
    last_used_at: null,
  },
];

describe('KeysPage', () => {
  beforeEach(() => {
    fetchMock.mockReset();
    routerMock.replace.mockReset();
    vi.stubGlobal('fetch', fetchMock);
    vi.stubEnv('NEXT_PUBLIC_API_BASE_URL', 'http://api.test');
    window.localStorage.clear();
  });
  afterEach(() => {
    vi.unstubAllGlobals();
    vi.unstubAllEnvs();
    vi.restoreAllMocks();
    window.localStorage.clear();
  });

  it('shows a loading state, then renders rows with metadata and status', async () => {
    fetchMock.mockResolvedValue(jsonResponse({ keys: sampleKeys }));
    render(<KeysPage />);

    expect(screen.getByText(/loading/i)).toBeInTheDocument();

    expect(await screen.findByText('dab_pk_abc')).toBeInTheDocument();
    expect(screen.getByText('Prod')).toBeInTheDocument();
    expect(
      screen.getByText('You are a helpful assistant.'),
    ).toBeInTheDocument();
    expect(screen.getByText('dab_pk_xyz')).toBeInTheDocument();
    expect(screen.getByText('Active')).toBeInTheDocument();
    expect(screen.getByText('Revoked')).toBeInTheDocument();
  });

  it('exposes the create entry point and a per-row edit affordance', async () => {
    fetchMock.mockResolvedValue(jsonResponse({ keys: sampleKeys }));
    render(<KeysPage />);
    await screen.findByText('dab_pk_abc');

    expect(screen.getByRole('link', { name: /new api key/i })).toHaveAttribute(
      'href',
      '/dashboard/keys/new',
    );
    expect(screen.getAllByRole('link', { name: /edit/i })[0]).toHaveAttribute(
      'href',
      '/dashboard/keys/1/edit',
    );
  });

  it('never renders a secret even if the API erroneously returns one', async () => {
    fetchMock.mockResolvedValue(
      jsonResponse({
        keys: [{ ...sampleKeys[0], secret: 'dab_sk_SHOULD_NOT_RENDER' }],
      }),
    );
    render(<KeysPage />);
    await screen.findByText('dab_pk_abc');

    expect(
      screen.queryByText(/dab_sk_SHOULD_NOT_RENDER/),
    ).not.toBeInTheDocument();
  });

  it('truncates a long persona behind an expander but keeps the full text in the DOM', async () => {
    const long = 'persona-'.repeat(20);
    fetchMock.mockResolvedValue(
      jsonResponse({ keys: [{ ...sampleKeys[0], persona: long }] }),
    );
    render(<KeysPage />);
    await screen.findByText('dab_pk_abc');

    expect(
      screen.getByText(
        (text) => text.startsWith('persona-') && text.endsWith('…'),
      ),
    ).toBeInTheDocument();
    expect(screen.getByText(long)).toBeInTheDocument();
  });

  it('renders an empty state when there are no keys', async () => {
    fetchMock.mockResolvedValue(jsonResponse({ keys: [] }));
    render(<KeysPage />);
    expect(await screen.findByText(/no keys yet/i)).toBeInTheDocument();
  });

  it('renders an error state with retry that recovers', async () => {
    fetchMock.mockResolvedValue(
      jsonResponse({ error: { type: 'internal_error', message: 'boom' } }, 500),
    );
    render(<KeysPage />);

    expect(await screen.findByRole('alert')).toHaveTextContent(
      /could not load/i,
    );

    fetchMock.mockResolvedValue(jsonResponse({ keys: sampleKeys }));
    fireEvent.click(screen.getByRole('button', { name: /retry/i }));
    expect(await screen.findByText('dab_pk_abc')).toBeInTheDocument();
  });

  it('redirects to /login when the list returns 401', async () => {
    fetchMock.mockResolvedValue(
      jsonResponse({ error: { type: 'unauthorized', message: 'nope' } }, 401),
    );
    render(<KeysPage />);
    await waitFor(() =>
      expect(routerMock.replace).toHaveBeenCalledWith('/login'),
    );
  });

  it('revokes a key after confirmation, then refreshes the list', async () => {
    fetchMock.mockResolvedValueOnce(jsonResponse({ keys: sampleKeys }));
    render(<KeysPage />);
    await screen.findByText('dab_pk_abc');

    vi.spyOn(window, 'confirm').mockReturnValue(true);
    fetchMock.mockResolvedValueOnce(new Response(null, { status: 204 })); // DELETE
    fetchMock.mockResolvedValueOnce(jsonResponse({ keys: sampleKeys })); // reload

    fireEvent.click(screen.getAllByRole('button', { name: /revoke/i })[0]);

    await waitFor(() => {
      const del = fetchMock.mock.calls.find(
        (c) => (c[1] as RequestInit | undefined)?.method === 'DELETE',
      );
      expect(del?.[0]).toBe('http://api.test/api/admin/keys/1');
    });
  });

  it('does not revoke when the confirmation is dismissed', async () => {
    fetchMock.mockResolvedValueOnce(jsonResponse({ keys: sampleKeys }));
    render(<KeysPage />);
    await screen.findByText('dab_pk_abc');

    vi.spyOn(window, 'confirm').mockReturnValue(false);
    fireEvent.click(screen.getAllByRole('button', { name: /revoke/i })[0]);

    const del = fetchMock.mock.calls.find(
      (c) => (c[1] as RequestInit | undefined)?.method === 'DELETE',
    );
    expect(del).toBeUndefined();
  });
});
