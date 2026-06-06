'use client';

import { useRouter } from 'next/navigation';
import { useState, type CSSProperties, type FormEvent } from 'react';

import { ApiError } from '@/lib/api-client';
import { createKey, type CreatedKey } from '@/lib/keys';
import { useAuthErrorRedirect } from '@/lib/use-auth';

export default function NewKeyPage() {
  const router = useRouter();
  const handleAuthError = useAuthErrorRedirect();

  const [name, setName] = useState('');
  const [persona, setPersona] = useState('');
  const [error, setError] = useState<string | null>(null);
  const [submitting, setSubmitting] = useState(false);
  const [created, setCreated] = useState<CreatedKey | null>(null);
  const [copied, setCopied] = useState(false);

  async function handleSubmit(event: FormEvent<HTMLFormElement>) {
    event.preventDefault();
    setError(null);
    if (name.trim() === '') {
      setError('Name is required.');
      return;
    }
    setSubmitting(true);
    try {
      const key = await createKey({
        name: name.trim(),
        persona: persona.trim() === '' ? null : persona,
      });
      setSubmitting(false);
      setCreated(key);
    } catch (err) {
      if (!handleAuthError(err)) {
        setError(messageForError(err));
      }
      setSubmitting(false);
    }
  }

  async function copySecret(secret: string) {
    try {
      await navigator.clipboard.writeText(secret);
      setCopied(true);
    } catch {
      setCopied(false);
    }
  }

  function done() {
    // Drop the plaintext secret from memory/DOM before leaving.
    setCreated(null);
    router.push('/dashboard/keys');
  }

  if (created !== null) {
    return (
      <section
        role="dialog"
        aria-modal="true"
        aria-label="API key created"
        style={panel}
      >
        <h1>API key created</h1>
        <p style={warning}>
          <strong>Copy the secret now.</strong> For security it is shown only
          once and cannot be retrieved again.
        </p>
        <dl>
          <dt>Public key id</dt>
          <dd>
            <code>{created.key_id}</code>
          </dd>
          <dt>Secret</dt>
          <dd>
            <code>{created.secret}</code>{' '}
            <button
              type="button"
              onClick={() => void copySecret(created.secret)}
            >
              Copy
            </button>{' '}
            {copied ? <span role="status">Copied</span> : null}
          </dd>
          {created.persona !== null && created.persona !== '' ? (
            <>
              <dt>Persona</dt>
              <dd style={{ whiteSpace: 'pre-wrap' }}>{created.persona}</dd>
            </>
          ) : null}
        </dl>
        <button type="button" onClick={done}>
          Done
        </button>
      </section>
    );
  }

  return (
    <section style={{ maxWidth: 560 }}>
      <h1>New API key</h1>
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
          Persona (optional system prompt)
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
            {submitting ? 'Creating…' : 'Create key'}
          </button>
          <button type="button" onClick={() => router.push('/dashboard/keys')}>
            Cancel
          </button>
        </div>
      </form>
    </section>
  );
}

function messageForError(err: unknown): string {
  if (err instanceof ApiError && err.status === 400) {
    return 'Please check the form and try again.';
  }
  return 'Could not create the key. Please try again.';
}

const panel: CSSProperties = {
  maxWidth: 560,
  border: '1px solid #e5e5e5',
  borderRadius: 6,
  padding: '1.5rem',
};
const warning: CSSProperties = {
  background: '#fff6e5',
  border: '1px solid #f0d29a',
  borderRadius: 4,
  padding: '0.5rem 0.75rem',
};
const label: CSSProperties = { display: 'block', margin: '0.75rem 0 0.25rem' };
const input: CSSProperties = {
  display: 'block',
  width: '100%',
  padding: '0.5rem',
  boxSizing: 'border-box',
  fontFamily: 'inherit',
};
