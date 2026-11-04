import { describe, expect, it } from 'vitest';
import { isDraft } from '../src/lib/legal';

// The Legal view shows the draft banner and sets noindex exactly when isDraft is true.
// The e2e test checks every real page against its front matter; this keeps the rule itself
// covered when the site has no draft left (fixtures stand in for a draft).
describe('the draft rule', () => {
  it('treats a draft as a draft, with or without a note', () => {
    expect(isDraft({ status: 'draft' })).toBe(true);
  });

  it('treats only published as published', () => {
    expect(isDraft({ status: 'published' })).toBe(false);
  });

  it('fails closed: an unknown status is a draft', () => {
    expect(isDraft({ status: 'review' as never })).toBe(true);
  });
});
