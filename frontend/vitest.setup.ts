import { afterEach } from 'vitest';
import { cleanup } from '@testing-library/react';
import '@testing-library/jest-dom/vitest';

// React Testing Library's auto-cleanup hooks into a global afterEach, which is
// not registered when Vitest globals are disabled. Register it explicitly so the
// DOM is torn down between tests.
afterEach(() => {
  cleanup();
});
