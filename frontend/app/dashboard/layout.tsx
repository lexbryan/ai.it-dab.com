'use client';

import type { CSSProperties, ReactNode } from 'react';

import { useLogout, useRequireAuth } from '@/lib/use-auth';

export default function DashboardLayout({ children }: { children: ReactNode }) {
  const session = useRequireAuth();
  const logout = useLogout();

  // Null while the client guard verifies the session (or redirects to /login).
  if (session === null) {
    return <main style={loadingStyle}>Loading…</main>;
  }

  return (
    <div style={{ fontFamily: 'system-ui, sans-serif' }}>
      <header style={headerStyle}>
        <strong>DAB AI</strong>
        <nav style={navStyle}>
          <span style={{ color: '#555' }}>{session.email}</span>
          <button
            type="button"
            onClick={() => void logout()}
            style={logoutStyle}
          >
            Log out
          </button>
        </nav>
      </header>
      <main style={{ padding: '2rem' }}>{children}</main>
    </div>
  );
}

const loadingStyle: CSSProperties = {
  padding: '2rem',
  fontFamily: 'system-ui, sans-serif',
};
const headerStyle: CSSProperties = {
  display: 'flex',
  justifyContent: 'space-between',
  alignItems: 'center',
  padding: '0.75rem 2rem',
  borderBottom: '1px solid #e5e5e5',
};
const navStyle: CSSProperties = {
  display: 'flex',
  gap: '1rem',
  alignItems: 'center',
};
const logoutStyle: CSSProperties = { padding: '0.4rem 0.8rem' };
