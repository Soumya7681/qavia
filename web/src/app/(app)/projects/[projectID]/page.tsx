import type { Metadata } from "next";

import { ProjectOverview } from "@/components/projects/project-overview";

export const metadata: Metadata = { title: "Overview" };

export default function ProjectPage() {
  return <ProjectOverview />;
}
