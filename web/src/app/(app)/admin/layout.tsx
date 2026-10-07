import { RequireRole } from "@/components/auth/require-role";

// Everything under /admin is for admins.
export default function AdminLayout({ children }: { children: React.ReactNode }) {
  return <RequireRole min="admin">{children}</RequireRole>;
}
