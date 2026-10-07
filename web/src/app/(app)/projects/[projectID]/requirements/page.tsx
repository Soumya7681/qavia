import type { Metadata } from "next";

import { SectionPending } from "@/components/projects/section-pending";

export const metadata: Metadata = { title: "Requirements" };

export default function Page() {
  return (
    <SectionPending
      title="Requirements are not shown here yet"
      description="This screen is being built. The API already extracts requirements from an uploaded specification."
    />
  );
}
