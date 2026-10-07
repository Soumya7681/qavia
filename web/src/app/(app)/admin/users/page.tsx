import type { Metadata } from "next";

import { UsersAdmin } from "@/components/admin/users-admin";

export const metadata: Metadata = { title: "Users" };

export default function UsersPage() {
  return <UsersAdmin />;
}
