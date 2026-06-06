import { afterEach, beforeEach, describe, expect, it } from 'vitest';

import {
  clearSession,
  getSession,
  isAuthenticated,
  setSession,
} from './session';

const STORAGE_KEY = 'dab.admin.session';

describe('session module', () => {
  beforeEach(() => window.localStorage.clear());
  afterEach(() => window.localStorage.clear());

  it('reports signed-out when empty', () => {
    expect(getSession()).toBeNull();
    expect(isAuthenticated()).toBe(false);
  });

  it('stores, reads, and clears a session', () => {
    setSession({ email: 'admin@dab.test', isSuperuser: true });

    expect(getSession()).toEqual({
      email: 'admin@dab.test',
      isSuperuser: true,
    });
    expect(isAuthenticated()).toBe(true);

    clearSession();
    expect(getSession()).toBeNull();
    expect(isAuthenticated()).toBe(false);
  });

  it('treats malformed or wrongly-typed data as no session and clears it', () => {
    window.localStorage.setItem(STORAGE_KEY, '{ not json');
    expect(getSession()).toBeNull();

    window.localStorage.setItem(STORAGE_KEY, JSON.stringify({ email: 123 }));
    expect(getSession()).toBeNull();
    // getSession clears the bad value as a side effect.
    expect(window.localStorage.getItem(STORAGE_KEY)).toBeNull();
  });

  it('persists only the identity, never a token or password', () => {
    setSession({ email: 'a@b.c', isSuperuser: false });
    const raw = window.localStorage.getItem(STORAGE_KEY) ?? '';
    expect(raw).not.toMatch(/token|jwt|password|secret/i);
  });
});
