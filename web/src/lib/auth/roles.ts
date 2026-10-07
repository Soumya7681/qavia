import type { components } from "@/lib/api/schema";

export type Role = components["schemas"]["Role"];
export type CurrentUser = components["schemas"]["User"];

// Mirrors api/internal/role. The order is the API's; this copy only decides what
// to show.
const rank: Record<Role, number> = {
  viewer: 0,
  qa_engineer: 1,
  qa_lead: 2,
  admin: 3,
};

/**
 * Whether to show something meant for `min` or above.
 *
 * Presentation only. The API authorises every request on its own, and a hidden
 * button is not a permission: a screen must still handle the 403 if the call is
 * made anyway (work-frontend.md FE-S.4).
 */
export function hasRole(user: Pick<CurrentUser, "role"> | null | undefined, min: Role): boolean {
  return user != null && rank[user.role] >= rank[min];
}
