import type { Metadata } from "next";

export const metadata: Metadata = { title: "Projects" };

// The list, search, and create dialog arrive in FE-0.5.
export default function ProjectsPage() {
  return <h1 className="text-xl font-semibold">Projects</h1>;
}
