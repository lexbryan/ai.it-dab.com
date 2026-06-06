'use client';

import Link from 'next/link';
import { useCallback, useEffect, useState, type CSSProperties } from 'react';

import { ApiError } from '@/lib/api-client';
import { isActive, listKeys, revokeKey, type ApiKeyMetadata } from '@/lib/keys';
import { useAuthErrorRedirect } from '@/lib/use-auth';

type LoadState =
  | { status: 'loading' }
  | { status: 'error' }
  | { status: 'ready'; keys: ApiKeyMetadata[] };

export default function KeysPage() {
  const handleAuthError = useAuthErrorRedirect();
  const [state, setState] = useState<LoadState>({ status: 'loading' });
  const [confirming, setConfirming] = useState<ApiKeyMetadata | null>(null);
  const [revoking, setRevoking] = useState(false);
  const [notice, setNotice] = useState<string | null>(null);
  const [actionError, setActionError] = useState<string | null>(null);

  const load = useCallback(async () => {
    setState({ status: 'loading' });
    try {
      const keys = await listKeys();
      setState({ status: 'ready', keys });
    } catch (err) {
      if (!handleAuthError(err)) {
        setState({ status: 'error' });
      }
    }
  }, [handleAuthError]);

  useEffect(() => {
    void load();
  }, [load]);

  function startRevoke(key: ApiKeyMetadata) {
    setNotice(null);
    setActionError(null);
    setConfirming(key);
  }

  async function confirmRevoke() {
    if (confirming === null) {
      return;
    }
    const key = confirming;
    setRevoking(true);
    setActionError(null);
    try {
      await revokeKey(key.id);
      setConfirming(null);
      setNotice(`Revoked ${key.key_id}.`);
      await load();
    } catch (err) {
      if (handleAuthError(err)) {
        return;
      }
      setConfirming(null);
      if (err instanceof ApiError && err.status === 404) {
        // Already gone — reconcile by refreshing; not a user-facing error.
        setNotice(`${key.key_id} was already revoked.`);
        await load();
      } else {
        // Keep the list as-is so it stays consistent; surface the failure.
        setActionError(`Could not revoke ${key.key_id}. Please try again.`);
      }
    } finally {
      setRevoking(false);
    }
  }

  return (
    <section>
      <div style={headerRow}>
        <h1>API keys</h1>
        <Link href="/dashboard/keys/new" style={newButton}>
          New API key
        </Link>
      </div>

      {notice !== null ? (
        <p role="status" style={noticeStyle}>
          {notice}
        </p>
      ) : null}
      {actionError !== null ? (
        <p role="alert" style={errorBanner}>
          {actionError}
        </p>
      ) : null}

      {state.status === 'loading' && <p>Loading…</p>}

      {state.status === 'error' && (
        <p role="alert">
          Could not load API keys.{' '}
          <button type="button" onClick={() => void load()}>
            Retry
          </button>
        </p>
      )}

      {state.status === 'ready' && state.keys.length === 0 && (
        <p>No keys yet. Create one to connect a project to the gateway.</p>
      )}

      {state.status === 'ready' && state.keys.length > 0 && (
        <table style={table}>
          <thead>
            <tr>
              <th style={th}>Key ID</th>
              <th style={th}>Name</th>
              <th style={th}>Persona</th>
              <th style={th}>Created</th>
              <th style={th}>Status</th>
              <th style={th}>Last used</th>
              <th style={th}>Actions</th>
            </tr>
          </thead>
          <tbody>
            {state.keys.map((key) => (
              <tr key={key.id}>
                <td style={td}>
                  <code>{key.key_id}</code>
                </td>
                <td style={td}>{key.name}</td>
                <td style={td}>
                  <PersonaCell persona={key.persona} />
                </td>
                <td style={td}>{formatDate(key.created_at)}</td>
                <td style={td}>{isActive(key) ? 'Active' : 'Revoked'}</td>
                <td style={td}>
                  {key.last_used_at ? formatDate(key.last_used_at) : 'Never'}
                </td>
                <td style={td}>
                  <Link href={`/dashboard/keys/${key.id}/edit`}>Edit</Link>
                  {isActive(key) && (
                    <>
                      {' · '}
                      <button type="button" onClick={() => startRevoke(key)}>
                        Revoke
                      </button>
                    </>
                  )}
                </td>
              </tr>
            ))}
          </tbody>
        </table>
      )}

      {confirming !== null ? (
        <div
          role="dialog"
          aria-modal="true"
          aria-label="Confirm revoke"
          style={dialog}
        >
          <p>
            Revoke <code>{confirming.key_id}</code>? Clients using this key will
            stop working immediately. This cannot be undone.
          </p>
          <div style={{ display: 'flex', gap: '0.75rem' }}>
            <button
              type="button"
              onClick={() => void confirmRevoke()}
              disabled={revoking}
            >
              {revoking ? 'Revoking…' : 'Revoke key'}
            </button>
            <button
              type="button"
              onClick={() => setConfirming(null)}
              disabled={revoking}
            >
              Cancel
            </button>
          </div>
        </div>
      ) : null}
    </section>
  );
}

/** Persona cell: the per-key system prompt, truncated behind an expander when
 * long. Never a secret — persona is non-sensitive label text. */
function PersonaCell({ persona }: { persona: string | null }) {
  if (persona === null || persona === '') {
    return <span>—</span>;
  }
  if (persona.length <= 60) {
    return <span>{persona}</span>;
  }
  return (
    <details>
      <summary>{`${persona.slice(0, 60)}…`}</summary>
      <div style={{ whiteSpace: 'pre-wrap', marginTop: '0.25rem' }}>
        {persona}
      </div>
    </details>
  );
}

function formatDate(iso: string): string {
  const date = new Date(iso);
  return Number.isNaN(date.getTime()) ? iso : date.toLocaleDateString();
}

const headerRow: CSSProperties = {
  display: 'flex',
  justifyContent: 'space-between',
  alignItems: 'center',
};
const newButton: CSSProperties = {
  padding: '0.4rem 0.8rem',
  border: '1px solid #ccc',
  borderRadius: 4,
  textDecoration: 'none',
};
const table: CSSProperties = {
  width: '100%',
  borderCollapse: 'collapse',
  marginTop: '1rem',
};
const th: CSSProperties = {
  textAlign: 'left',
  borderBottom: '2px solid #e5e5e5',
  padding: '0.5rem',
};
const td: CSSProperties = {
  borderBottom: '1px solid #efefef',
  padding: '0.5rem',
  verticalAlign: 'top',
};
const noticeStyle: CSSProperties = {
  background: '#e8f5e9',
  border: '1px solid #b6dab9',
  borderRadius: 4,
  padding: '0.5rem 0.75rem',
};
const errorBanner: CSSProperties = {
  background: '#fdecea',
  border: '1px solid #f5c2c0',
  borderRadius: 4,
  padding: '0.5rem 0.75rem',
  color: '#7a1c17',
};
const dialog: CSSProperties = {
  marginTop: '1rem',
  maxWidth: 480,
  border: '1px solid #e5e5e5',
  borderRadius: 6,
  padding: '1rem 1.25rem',
};
