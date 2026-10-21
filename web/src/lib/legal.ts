// Legal documents in content/legal/<locale>/: privacy and terms are versioned
// (<doc>/<version>.mdx, every version stays published at /<doc>/<version>), the legal
// notice and support are single files (<doc>.mdx).
import { existsSync } from 'node:fs';
import path from 'node:path';
import { CONTENT_DIR, listMdx, loadMdx } from './content';
import type { Locale } from './i18n';

export const VERSIONED = ['privacy', 'terms'] as const;
export const SINGLE = ['impressum', 'support'] as const;
export type VersionedDoc = (typeof VERSIONED)[number];
export type LegalDoc = VersionedDoc | (typeof SINGLE)[number];

export type LegalFrontMatter = {
  title: string;
  /** For privacy: equals the platform's PRIVACY_POLICY_VERSION (consent is recorded against it). */
  version: string;
  /** ISO date the version took effect; null while it's a draft. */
  effective: string | null;
  /** Only the user flips draft → published, after review. */
  status: 'draft' | 'published';
  /** Text that belongs next to the version (privacy: that the app asks for consent to it). */
  versionNote?: string;
  /** The draft's own note, shown in the banner while status is draft. */
  draftNote?: string;
  /** The platform draft this was ported from; tests compare the text while it is set. */
  portedFrom?: string;
};

export function isVersioned(doc: LegalDoc): doc is VersionedDoc {
  return (VERSIONED as readonly string[]).includes(doc);
}

/** Versions of a versioned document, newest first. */
export async function versionsOf(locale: Locale, doc: VersionedDoc): Promise<string[]> {
  return listMdx(`legal/${locale}/${doc}`);
}

export async function loadLegal(locale: Locale, doc: LegalDoc, version?: string) {
  if (!isVersioned(doc)) return loadMdx<LegalFrontMatter>(`legal/${locale}/${doc}.mdx`);
  const v = version ?? (await versionsOf(locale, doc))[0];
  const rel = `legal/${locale}/${doc}/${v}.mdx`;
  if (!existsSync(path.join(CONTENT_DIR, rel))) throw new Error(`${rel} does not exist`);
  return loadMdx<LegalFrontMatter>(rel);
}

/** Static params for /<doc>/[version]: every version in English (the root locale). */
export async function versionParams(doc: VersionedDoc) {
  return (await versionsOf('en', doc)).map((version) => ({ version }));
}
