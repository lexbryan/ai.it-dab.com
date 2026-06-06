'use client';

import { useRouter } from 'next/navigation';
import { useState, type CSSProperties, type FormEvent } from 'react';

import { ApiError } from '@/lib/api-client';
import { login } from '@/lib/auth';

export default function LoginPage() {
  const router = useRouter();
  const [email, setEmail] = useState('');
  const [password, setPassword] = useState('');
  const [error, setError] = useState<string | null>(null);
  const [submitting, setSubmitting] = useState(false);

  async function handleSubmit(event: FormEvent<HTMLFormElement>) {
    event.preventDefault();
    setError(null);

    if (email.trim() === '' || password === '') {
      setError('Email and password are required.');
      return;
    }

    setSubmitting(true);
    try {
      await login(email.trim(), password);
      // Navigate to the authenticated shell; this component unmounts.
      router.push('/dashboard');
    } catch (err) {
      setError(messageForError(err));
      setSubmitting(false);
    }
  }

  return (
    <main style={pageStyle}>
      <h1>Sign in</h1>
      <p style={{ color: '#555', marginTop: 0 }}>DAB AI admin console</p>
      <form onSubmit={handleSubmit} noValidate>
        <label style={labelStyle}>
          Email
          <input
            type="email"
            name="email"
            autoComplete="username"
            value={email}
            onChange={(event) => setEmail(event.target.value)}
            disabled={submitting}
            style={inputStyle}
          />
        </label>
        <label style={labelStyle}>
          Password
          <input
            type="password"
            name="password"
            autoComplete="current-password"
            value={password}
            onChange={(event) => setPassword(event.target.value)}
            disabled={submitting}
            style={inputStyle}
          />
        </label>
        {error !== null ? (
          <p role="alert" style={errorStyle}>
            {error}
          </p>
        ) : null}
        <button type="submit" disabled={submitting} style={buttonStyle}>
          {submitting ? 'Signing in…' : 'Sign in'}
        </button>
      </form>
    </main>
  );
}

/** Map a failed login into a user-safe message (never leaks server detail). */
function messageForError(err: unknown): string {
  if (err instanceof ApiError) {
    if (err.status === 429) {
      return 'Too many attempts. Please wait a moment and try again.';
    }
    if (err.status === 401) {
      return 'Invalid email or password.';
    }
  }
  return 'Something went wrong. Please try again.';
}

const pageStyle: CSSProperties = {
  maxWidth: 360,
  margin: '4rem auto',
  padding: '0 1rem',
  fontFamily: 'system-ui, sans-serif',
};
const labelStyle: CSSProperties = {
  display: 'block',
  margin: '0.75rem 0 0.25rem',
};
const inputStyle: CSSProperties = {
  display: 'block',
  width: '100%',
  padding: '0.5rem',
  boxSizing: 'border-box',
};
const errorStyle: CSSProperties = { color: '#b00020', margin: '0.75rem 0 0' };
const buttonStyle: CSSProperties = {
  marginTop: '1rem',
  padding: '0.5rem 1rem',
};
