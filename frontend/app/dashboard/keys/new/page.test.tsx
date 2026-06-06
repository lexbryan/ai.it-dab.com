import {
  fireEvent,
  render,
  screen,
  waitFor,
  within,
} from '@testing-library/react';
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest';

const { routerMock } = vi.hoisted(() => ({
  routerMock: { replace: vi.fn(), push: vi.fn() },
}));
vi.mock('next/navigation', () => ({ useRouter: () => routerMock }));

import NewKeyPage from './page';

const fetchMock = vi.fn<typeof fetch>();
const clipboardWrite = vi.fn<(text: string) => Promise<void>>();

function jsonResponse(body: unknown, status = 200): Response {
  return new Response(JSON.stringify(body), {
    status,
    headers: { 'content-type': 'application/json' },
  });
}

const created = {
  id: '1',
  key_id: 'dab_pk_abc',
  secret: 'dab_sk_TOPSECRET',
  name: 'Prod',
  persona: 'You are a helpful assistant.',
  created_at: '2026-01-01T00:00:00Z',
};

function fillForm(name: string, persona: string) {
  fireEvent.change(screen.getByLabelText(/name/i), {
    target: { value: name },
  });
  fireEvent.change(screen.getByLabelText(/persona/i), {
    target: { value: persona },
  });
}

describe('NewKeyPage', () => {
  beforeEach(() => {
    fetchMock.mockReset();
    routerMock.push.mockReset();
    routerMock.replace.mockReset();
    clipboardWrite.mockReset();
    clipboardWrite.mockResolvedValue(undefined);
    vi.stubGlobal('fetch', fetchMock);
    vi.stubEnv('NEXT_PUBLIC_API_BASE_URL', 'http://api.test');
    Object.defineProperty(navigator, 'clipboard', {
      value: { writeText: clipboardWrite },
      configurable: true,
    });
    window.localStorage.clear();
  });
  afterEach(() => {
    vi.unstubAllGlobals();
    vi.unstubAllEnvs();
    window.localStorage.clear();
  });

  it('requires a name before calling the API', () => {
    render(<NewKeyPage />);
    fireEvent.click(screen.getByRole('button', { name: /create key/i }));
    expect(screen.getByRole('alert')).toHaveTextContent(/name is required/i);
    expect(fetchMock).not.toHaveBeenCalled();
  });

  it('creates a key and reveals the secret + key id exactly once', async () => {
    fetchMock.mockResolvedValue(jsonResponse(created, 201));
    render(<NewKeyPage />);
    fillForm('Prod', 'You are a helpful assistant.');
    fireEvent.click(screen.getByRole('button', { name: /create key/i }));

    const dialog = await screen.findByRole('dialog');
    expect(within(dialog).getByText('dab_pk_abc')).toBeInTheDocument();
    expect(within(dialog).getByText('dab_sk_TOPSECRET')).toBeInTheDocument();
    expect(
      within(dialog).getByText('You are a helpful assistant.'),
    ).toBeInTheDocument();
    expect(within(dialog).getByText(/shown only once/i)).toBeInTheDocument();

    // POST body matches the contract.
    const [url, init] = fetchMock.mock.calls[0];
    expect(url).toBe('http://api.test/api/admin/keys');
    expect(init?.method).toBe('POST');
    expect(init?.body).toBe(
      JSON.stringify({ name: 'Prod', persona: 'You are a helpful assistant.' }),
    );
  });

  it('copies the secret to the clipboard', async () => {
    fetchMock.mockResolvedValue(jsonResponse(created, 201));
    render(<NewKeyPage />);
    fillForm('Prod', '');
    fireEvent.click(screen.getByRole('button', { name: /create key/i }));
    await screen.findByRole('dialog');

    fireEvent.click(screen.getByRole('button', { name: /^copy$/i }));
    await waitFor(() =>
      expect(clipboardWrite).toHaveBeenCalledWith('dab_sk_TOPSECRET'),
    );
    expect(await screen.findByRole('status')).toHaveTextContent(/copied/i);
  });

  it('drops the secret from the DOM and navigates to the list on Done', async () => {
    fetchMock.mockResolvedValue(jsonResponse(created, 201));
    render(<NewKeyPage />);
    fillForm('Prod', '');
    fireEvent.click(screen.getByRole('button', { name: /create key/i }));
    await screen.findByRole('dialog');

    fireEvent.click(screen.getByRole('button', { name: /done/i }));

    expect(screen.queryByText('dab_sk_TOPSECRET')).not.toBeInTheDocument();
    expect(screen.queryByRole('dialog')).not.toBeInTheDocument();
    expect(routerMock.push).toHaveBeenCalledWith('/dashboard/keys');
  });

  it('surfaces an error inline and reveals no secret when creation fails', async () => {
    fetchMock.mockResolvedValue(
      jsonResponse({ error: { type: 'internal_error', message: 'boom' } }, 500),
    );
    render(<NewKeyPage />);
    fillForm('Prod', '');
    fireEvent.click(screen.getByRole('button', { name: /create key/i }));

    await waitFor(() =>
      expect(screen.getByRole('alert')).toHaveTextContent(/could not create/i),
    );
    expect(screen.queryByRole('dialog')).not.toBeInTheDocument();
    expect(screen.queryByText(/dab_sk_/)).not.toBeInTheDocument();
  });

  it('redirects to /login on a 401', async () => {
    fetchMock.mockResolvedValue(
      jsonResponse({ error: { type: 'unauthorized', message: 'nope' } }, 401),
    );
    render(<NewKeyPage />);
    fillForm('Prod', '');
    fireEvent.click(screen.getByRole('button', { name: /create key/i }));

    await waitFor(() =>
      expect(routerMock.replace).toHaveBeenCalledWith('/login'),
    );
    expect(screen.queryByRole('dialog')).not.toBeInTheDocument();
  });
});
