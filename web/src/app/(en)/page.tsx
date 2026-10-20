import { PageShell } from '@/components/chrome';
import { Container, Section } from '@/components/ui';

export default function Page() {
  return (
    <PageShell locale="en" path="/">
      <Container>
        <Section headingLevel={1} title="Your whole health." />
      </Container>
    </PageShell>
  );
}
