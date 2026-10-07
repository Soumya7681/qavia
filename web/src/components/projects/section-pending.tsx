import { HourglassIcon } from "lucide-react";

import { EmptyState } from "@/components/ui/empty-state";

/** A project section whose screen is not built yet, said plainly rather than faked. */
export function SectionPending({ title, description }: { title: string; description: string }) {
  return <EmptyState icon={HourglassIcon} title={title} description={description} />;
}
