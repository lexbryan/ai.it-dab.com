'use client';

import Link from 'next/link';
import { useCallback, useEffect, useState, type CSSProperties } from 'react';

import { isActive, listKeys, revokeKey, type ApiKeyMetadata } from '@/lib/keys';
import { useAuthErrorRedirect } from '@/lib/use-auth';

type LoadState =
  | { status: 'loading' }
  | { status: 'error' }
  | { status: 'ready'; keys: ApiKeyMetadata[] };

export default function KeysPage() {
  const handleAuthError = useAuthErrorRedirect();
  const [state, setState] = useState<LoadState>({ status: 'loading' });

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

  async function onRevoke(key: ApiKeyMetadata) {
    if (
      !window.confirm(
        `Revoke "${key.name}"? Clients using this key will stop working.`,
      )
    ) {
      return;
    }
    try {
      await revokeKey(key.id);
      await load();
    } catch (err) {
      if (!handleAuthError(err)) {
        setState({ status: 'error' });
      }
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
                      <button type="button" onClick={() => void onRevoke(key)}>
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
