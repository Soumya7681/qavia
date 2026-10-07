import { LockIcon } from "lucide-react";
import Link from "next/link";

import { Button } from "@/components/ui/button";
import { EmptyState } from "@/components/ui/empty-state";

// Copy is chosen from the error code, never read from a URL or a response body
// passed through one: a message taken from a query string is one anyone can write.
const copy: Record<string, { title: string; description: string }> = {
  role_required: {
    title: "Your role does not include this",
    description: "Ask an admin to change your role if you need it.",
  },
  not_project_member: {
    title: "You are not a member of this project",
    description: "Ask the project owner or an admin to add you.",
  },
  forbidden: {
    title: "You do not have access to this",
    description: "Ask an admin if you think you should.",
  },
};

export function NoAccess({ reason }: { reason?: string }) {
  const { title, description } = (reason && copy[reason]) || copy.forbidden;
  return (
    <div className="mx-auto max-w-xl pt-12">
      <EmptyState
        icon={LockIcon}
        title={title}
        description={description}
        action={
          <Button asChild variant="outline" size="sm">
            <Link href="/">Back to projects</Link>
          </Button>
        }
      />
    </div>
  );
}
