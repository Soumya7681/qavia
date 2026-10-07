import type { Metadata } from "next";

import { SectionPending } from "@/components/projects/section-pending";

export const metadata: Metadata = { title: "Files" };

export default function Page() {
  return (
    <SectionPending
      title="Files are not shown here yet"
      description="This screen is being built. Generated test code will be browsable and downloadable here."
    />
  );
}
