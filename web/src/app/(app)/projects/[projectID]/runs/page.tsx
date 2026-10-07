import type { Metadata } from "next";

import { SectionPending } from "@/components/projects/section-pending";

export const metadata: Metadata = { title: "Runs" };

export default function Page() {
  return (
    <SectionPending
      title="Runs are not shown here yet"
      description="This screen is being built. Runs and their live logs will appear here."
    />
  );
}
