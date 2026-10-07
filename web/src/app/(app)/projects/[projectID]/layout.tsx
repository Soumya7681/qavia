import { NoAccess } from "@/components/auth/no-access";
import { getProject } from "@/lib/auth/server";

// Membership is checked for the whole project subtree here, so no project page
// renders for someone who is not in the project. The (app) layout above has already
// established who they are.
export default async function ProjectLayout({
  children,
  params,
}: LayoutProps<"/projects/[projectID]">) {
  const { projectID } = await params;
  const access = await getProject(projectID);
  if (access.denied) {
    return <NoAccess reason={access.denied} />;
  }
  return children;
}
