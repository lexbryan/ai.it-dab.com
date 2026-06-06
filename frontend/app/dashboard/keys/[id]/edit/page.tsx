'use client';

import { useParams, useRouter } from 'next/navigation';
import {
  useCallback,
  useEffect,
  useState,
  type CSSProperties,
  type FormEvent,
} from 'react';

import { getKey, updateKey } from '@/lib/keys';
import { useAuthErrorRedirect } from '@/lib/use-auth';

type LoadState = 'loading' | 'ready' | 'notfound' | 'error';

export default function EditKeyPage() {
  const router = useRouter();
  const params = useParams<{ id: string }>();
  const id = params.id;
  const handleAuthError = useAuthErrorRedirect();

  const [loadState, setLoadState] = useState<LoadState>('loading');
  const [keyId, setKeyId] = useState('');
  const [name, setName] = useState('');
  const [persona, setPersona] = useState('');
  const [error, setError] = useState<string | null>(null);
  const [submitting, setSubmitting] = useState(false);

  const load = useCallback(async () => {
    setLoadState('loading');
    try {
      const key = await getKey(id);
      if (key === null) {
        setLoadState('notfound');
        return;
      }
      setKeyId(key.key_id);
      setName(key.name);
      setPersona(key.persona ?? '');
      setLoadState('ready');
    } catch (err) {
      if (!handleAuthError(err)) {
        setLoadState('error');
      }
    }
  }, [id, handleAuthError]);

  useEffect(() => {
    void load();
  }, [load]);

  async function handleSubmit(event: FormEvent<HTMLFormElement>) {
    event.preventDefault();
    setError(null);
    if (name.trim() === '') {
      setError('Name is required.');
      return;
    }
    setSubmitting(true);
    try {
      await updateKey(id, { name: name.trim(), persona });
      router.push('/dashboard/keys');
    } catch (err) {
      if (!handleAuthError(err)) {
        setError('Could not save changes. Please try again.');
      }
      setSubmitting(false);
    }
  }

  if (loadState === 'loading') {
    return <p>Loading…</p>;
  }
  if (loadState === 'notfound') {
    return <p>That API key no longer exists.</p>;
  }
  if (loadState === 'error') {
    return (
      <p role="alert">
        Could not load the API key.{' '}
        <button type="button" onClick={() => void load()}>
          Retry
        </button>
      </p>
    );
  }

  return (
    <section style={{ maxWidth: 560 }}>
      <h1>Edit API key</h1>
      <p>
        <code>{keyId}</code>
      </p>
      <form onSubmit={handleSubmit} noValidate>
        <label style={label}>
          Name
          <input
            type="text"
            value={name}
            onChange={(event) => setName(event.target.value)}
            disabled={submitting}
            style={input}
          />
        </label>
        <label style={label}>
          Persona (system prompt)
          <textarea
            value={persona}
            onChange={(event) => setPersona(event.target.value)}
            disabled={submitting}
            rows={6}
            style={input}
          />
        </label>
        {error !== null ? (
          <p role="alert" style={{ color: '#b00020' }}>
            {error}
          </p>
        ) : null}
        <div style={{ marginTop: '1rem', display: 'flex', gap: '0.75rem' }}>
          <button type="submit" disabled={submitting}>
            {submitting ? 'Saving…' : 'Save changes'}
          </button>
          <button type="button" onClick={() => router.push('/dashboard/keys')}>
            Cancel
          </button>
        </div>
      </form>
    </section>
  );
}

const label: CSSProperties = { display: 'block', margin: '0.75rem 0 0.25rem' };
const input: CSSProperties = {
  display: 'block',
  width: '100%',
  padding: '0.5rem',
  boxSizing: 'border-box',
  fontFamily: 'inherit',
};
