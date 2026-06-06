import { render, screen } from '@testing-library/react';
import { describe, expect, it } from 'vitest';

import Home from './page';

describe('Home page', () => {
  it('renders the gateway heading', () => {
    render(<Home />);
    expect(
      screen.getByRole('heading', { name: /dab ai gateway/i }),
    ).toBeInTheDocument();
  });
});
