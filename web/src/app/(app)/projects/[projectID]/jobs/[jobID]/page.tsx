import type { Metadata } from "next";
import { Suspense } from "react";

import { JobView } from "@/components/jobs/job-view";

export const metadata: Metadata = { title: "Job" };

const UUID = /^[0-9a-f-]{36}$/i;

async function Job({ params, searchParams }: PageProps<"/projects/[projectID]/jobs/[jobID]">) {
  const [{ projectID, jobID }, query] = await Promise.all([params, searchParams]);
  const artifact =
    typeof query.artifact === "string" && UUID.test(query.artifact) ? query.artifact : undefined;
  return (
    <JobView
      projectID={projectID}
      jobID={jobID}
      retry={artifact ? { artifactId: artifact } : undefined}
    />
  );
}

export default function JobPage(props: PageProps<"/projects/[projectID]/jobs/[jobID]">) {
  return (
    <Suspense>
      <Job {...props} />
    </Suspense>
  );
}
