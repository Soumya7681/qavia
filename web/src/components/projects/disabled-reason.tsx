import type { Permission } from "./project-context";

/** The sentence beside a disabled control, so a disabled button is never a mystery. */
export function DisabledReason({ permission, id }: { permission: Permission; id?: string }) {
  if (permission.allowed) return null;
  return (
    <p id={id} className="text-xs text-muted-foreground">
      {permission.reason}
    </p>
  );
}
