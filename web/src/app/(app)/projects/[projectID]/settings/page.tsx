import type { Metadata } from "next";

import { ProjectSettings } from "@/components/projects/project-settings";

export const metadata: Metadata = { title: "Project settings" };

export default function ProjectSettingsPage() {
  return <ProjectSettings />;
}
