import { SciencePage, scienceMetadata } from '@/views/Science';

export const generateMetadata = () => scienceMetadata('en');

export default function Page() {
  return <SciencePage locale="en" />;
}
