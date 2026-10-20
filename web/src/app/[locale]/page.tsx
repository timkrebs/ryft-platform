import { PageShell } from '@/components/chrome';
import { Container, Section } from '@/components/ui';
import { localeFrom, type LocaleParams } from '@/lib/routing';

export default async function Page({ params }: LocaleParams) {
  const locale = await localeFrom(params);
  return (
    <PageShell locale={locale} path="/">
      <Container>
        <Section headingLevel={1} title="Deine ganze Gesundheit." />
      </Container>
    </PageShell>
  );
}
