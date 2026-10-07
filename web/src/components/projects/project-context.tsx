"use client";

import { createContext, useContext } from "react";

import { useCurrentUser } from "@/components/auth/current-user";
import type { components } from "@/lib/api/schema";
import { hasRole, type Role } from "@/lib/auth/roles";

export type Project = components["schemas"]["Project"];

const ProjectContext = createContext<Project | null>(null);

export function ProjectProvider({
  project,
  children,
}: {
  project: Project;
  children: React.ReactNode;
}) {
  return <ProjectContext.Provider value={project}>{children}</ProjectContext.Provider>;
}

export function useProject(): Project {
  const project = useContext(ProjectContext);
  if (!project) throw new Error("useProject is only available inside a project's layout");
  return project;
}

export type Permission = { allowed: true; reason?: never } | { allowed: false; reason: string };

/**
 * Whether a mutating control in this project should be enabled, and if not, the
 * reason to show beside it (FE-0.5). An archived project is read-only for
 * everyone; otherwise the role decides. Presentation only: the API refuses
 * regardless.
 */
export function useCanMutate(min: Role = "qa_engineer"): Permission {
  const project = useContext(ProjectContext);
  const user = useCurrentUser();
  if (project?.archived) {
    return {
      allowed: false,
      reason: "This project is archived and read-only. Unarchive it to make changes.",
    };
  }
  if (!hasRole(user, min)) {
    return {
      allowed: false,
      reason:
        min === "qa_lead"
          ? "Only a QA Lead or an admin can do this."
          : min === "admin"
            ? "Only an admin can do this."
            : "Viewers cannot make changes.",
    };
  }
  return { allowed: true };
}
