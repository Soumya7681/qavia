import type { Metadata } from "next";

import { SectionPending } from "@/components/projects/section-pending";

export const metadata: Metadata = { title: "Defects" };

export default function Page() {
  return (
    <SectionPending
      title="Defects are not shown here yet"
      description="This screen is being built. Defects filed from failed runs will appear here."
    />
  );
}
