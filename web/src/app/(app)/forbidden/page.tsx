import type { Metadata } from "next";
import { Suspense } from "react";

import { NoAccess } from "@/components/auth/no-access";

export const metadata: Metadata = { title: "No access" };

async function NoAccessFor({
  searchParams,
}: {
  searchParams: PageProps<"/forbidden">["searchParams"];
}) {
  const { reason } = await searchParams;
  return <NoAccess reason={typeof reason === "string" ? reason : undefined} />;
}

// Where a query that comes back "no access" sends the user (FE-S.3). The generic
// message is the static shell; the specific one streams in once the reason is read.
export default function ForbiddenPage({ searchParams }: PageProps<"/forbidden">) {
  return (
    <Suspense fallback={<NoAccess />}>
      <NoAccessFor searchParams={searchParams} />
    </Suspense>
  );
}
