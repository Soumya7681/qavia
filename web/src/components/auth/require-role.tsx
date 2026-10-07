import { NoAccess } from "@/components/auth/no-access";
import { getCurrentUser } from "@/lib/auth/server";
import { hasRole, type Role } from "@/lib/auth/roles";

/**
 * A page-level role gate for screens a role cannot use at all, so they see the
 * no-access state instead of a page of 403s. The API still decides every call.
 */
export async function RequireRole({ min, children }: { min: Role; children: React.ReactNode }) {
  const user = await getCurrentUser();
  if (!hasRole(user, min)) return <NoAccess reason="role_required" />;
  return children;
}
