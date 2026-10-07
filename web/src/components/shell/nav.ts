import { FolderKanbanIcon, type LucideIcon, SettingsIcon } from "lucide-react";

import type { Role } from "@/lib/auth/roles";

export type NavItem = { href: string; label: string; icon: LucideIcon; min?: Role };

/**
 * The global navigation. An entry is listed only once its screen exists, so the
 * sidebar never links to a page that is not there; `min` hides what a role cannot
 * use, and the API refuses it regardless.
 */
export const workspaceNav: NavItem[] = [
  { href: "/projects", label: "Projects", icon: FolderKanbanIcon },
  { href: "/settings", label: "Settings", icon: SettingsIcon },
];

export const adminNav: NavItem[] = [];

/** Labels for path segments, for breadcrumbs. Anything not here is title-cased. */
export const segmentLabels: Record<string, string> = {
  projects: "Projects",
  notifications: "Notifications",
  admin: "Admin",
  users: "Users",
  settings: "Settings",
  ai: "AI providers",
  integrations: "Integrations",
  audit: "Audit log",
  account: "Account",
  requirements: "Requirements",
  "test-cases": "Test cases",
  files: "Files",
  runs: "Runs",
  defects: "Defects",
  jobs: "Jobs",
  coverage: "Coverage",
  repository: "Repository",
  "test-data": "Test data",
  mock: "Mock server",
  performance: "Performance",
  security: "Security",
  drift: "Drift",
  forbidden: "No access",
};
