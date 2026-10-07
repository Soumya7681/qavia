import type { Metadata } from "next";
import { Suspense } from "react";

import { ProjectList } from "@/components/projects/project-list";

export const metadata: Metadata = { title: "Projects" };

export default function ProjectsPage() {
  return (
    <Suspense>
      <ProjectList />
    </Suspense>
  );
}
