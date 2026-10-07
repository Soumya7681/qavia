import type { Metadata } from "next";

import { SectionPending } from "@/components/projects/section-pending";

export const metadata: Metadata = { title: "Test cases" };

export default function Page() {
  return (
    <SectionPending
      title="Test cases are not shown here yet"
      description="This screen is being built. Generated test cases can be reviewed here once it lands."
    />
  );
}
