import { getProject } from "@/lib/auth/server";

// The project overview arrives with FE-0.5. Until then this proves the membership
// guard: it renders only for a member.
export default async function ProjectPage({ params }: PageProps<"/projects/[projectID]">) {
  const { projectID } = await params;
  const { project } = await getProject(projectID);
  return <h1 className="text-xl font-semibold">{project?.name}</h1>;
}
