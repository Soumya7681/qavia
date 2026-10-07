import { ArchiveIcon } from "lucide-react";

import { NoAccess } from "@/components/auth/no-access";
import { ProjectProvider } from "@/components/projects/project-context";
import { ProjectTabs } from "@/components/projects/project-tabs";
import { RememberProject } from "@/components/shell/remember-project";
import { getProject } from "@/lib/auth/server";

// Membership is checked for the whole project subtree here, so no project page
// renders for someone who is not in the project (FE-S.4). The header and tabs are
// the frame every later phase fills.
export default async function ProjectLayout({
  children,
  params,
}: LayoutProps<"/projects/[projectID]">) {
  const { projectID } = await params;
  const access = await getProject(projectID);
  if (access.denied !== undefined) {
    return <NoAccess reason={access.denied} />;
  }
  const { project } = access;

  return (
    <ProjectProvider project={project}>
      <RememberProject projectID={projectID} />
      <div className="flex flex-col gap-6">
        <header className="flex flex-col gap-3 border-b">
          <div className="flex flex-col gap-1">
            <h1 className="text-xl font-semibold">{project.name}</h1>
            {project.description ? (
              <p className="max-w-prose text-sm text-muted-foreground">{project.description}</p>
            ) : null}
          </div>
          {project.archived ? (
            <div
              role="status"
              className="flex items-center gap-2 rounded-md border border-warning/30 bg-warning-wash px-3 py-2 text-sm text-warning"
            >
              <ArchiveIcon aria-hidden className="size-4" />
              This project is archived. Everything is read-only until a QA Lead or an admin
              unarchives it.
            </div>
          ) : null}
          <ProjectTabs projectID={projectID} />
        </header>
        {children}
      </div>
    </ProjectProvider>
  );
}
