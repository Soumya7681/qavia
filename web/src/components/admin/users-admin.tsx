"use client";

import { zodResolver } from "@hookform/resolvers/zod";
import { useQueryClient } from "@tanstack/react-query";
import { CopyIcon, MoreHorizontalIcon, UserPlusIcon, UsersIcon } from "lucide-react";
import { useState } from "react";
import { Controller, useForm } from "react-hook-form";
import { toast } from "sonner";
import { z } from "zod";

import { useCurrentUser } from "@/components/auth/current-user";
import { FormAlert } from "@/components/auth/form-alert";
import { roleLabel } from "@/components/shell/user-menu";
import { Badge } from "@/components/ui/badge";
import { Button } from "@/components/ui/button";
import {
  Dialog,
  DialogContent,
  DialogDescription,
  DialogFooter,
  DialogHeader,
  DialogTitle,
} from "@/components/ui/dialog";
import {
  DropdownMenu,
  DropdownMenuContent,
  DropdownMenuItem,
  DropdownMenuLabel,
  DropdownMenuRadioGroup,
  DropdownMenuRadioItem,
  DropdownMenuSeparator,
  DropdownMenuTrigger,
} from "@/components/ui/dropdown-menu";
import { EmptyState } from "@/components/ui/empty-state";
import { ErrorState } from "@/components/ui/error-state";
import { Field, FieldError, FieldGroup, FieldLabel } from "@/components/ui/field";
import { Input } from "@/components/ui/input";
import {
  Select,
  SelectContent,
  SelectItem,
  SelectTrigger,
  SelectValue,
} from "@/components/ui/select";
import { Skeleton } from "@/components/ui/skeleton";
import {
  Table,
  TableBody,
  TableCell,
  TableHead,
  TableHeader,
  TableRow,
} from "@/components/ui/table";
import { api } from "@/lib/api/client";
import { type ApiError, isApiError } from "@/lib/api/errors";
import type { components } from "@/lib/api/schema";
import type { Role } from "@/lib/auth/roles";
import { useCursorList } from "@/lib/hooks/use-cursor-list";
import { useFormat } from "@/lib/hooks/use-format";

type Invitation = components["schemas"]["Invitation"];
const roles: Role[] = ["viewer", "qa_engineer", "qa_lead", "admin"];

const roleHelp: Record<Role, string> = {
  viewer: "Reads everything they are a member of. Changes nothing.",
  qa_engineer: "Uploads, generates, reviews, and runs in their projects.",
  qa_lead: "Everything an engineer does, plus members and archiving, across all projects.",
  admin: "Everything, plus platform settings, AI providers, and accounts.",
};

const inviteSchema = z.object({
  email: z.email("Enter their work email."),
  role: z.enum(["viewer", "qa_engineer", "qa_lead", "admin"]),
});

function InviteDialog({
  open,
  onOpenChange,
}: {
  open: boolean;
  onOpenChange: (o: boolean) => void;
}) {
  const queryClient = useQueryClient();
  const format = useFormat();
  const [failure, setFailure] = useState<ApiError | null>(null);
  const [invitation, setInvitation] = useState<Invitation | null>(null);
  const invite = api.useMutation("post", "/api/v1/users/invite");
  const form = useForm<z.infer<typeof inviteSchema>>({
    resolver: zodResolver(inviteSchema),
    defaultValues: { email: "", role: "qa_engineer" },
  });

  const close = (next: boolean) => {
    if (!next) {
      setInvitation(null);
      setFailure(null);
      form.reset();
    }
    onOpenChange(next);
  };

  const onSubmit = form.handleSubmit(async (values) => {
    setFailure(null);
    try {
      setInvitation(await invite.mutateAsync({ body: values }));
      await queryClient.invalidateQueries({ queryKey: ["get", "/api/v1/users"] });
    } catch (error) {
      if (!isApiError(error)) throw error;
      if (error.code === "email_already_taken") {
        form.setError("email", { message: "Someone already has an account with this email." });
      } else setFailure(error);
    }
  });

  return (
    <Dialog open={open} onOpenChange={close}>
      <DialogContent>
        {invitation ? (
          <div className="flex flex-col gap-4">
            <DialogHeader>
              <DialogTitle>Invitation ready</DialogTitle>
              <DialogDescription>
                Send this link to {invitation.user.email}. It is shown once and cannot be retrieved
                later. It expires {format.relative(invitation.expiresAt)}.
              </DialogDescription>
            </DialogHeader>
            <div className="flex gap-2">
              <Input
                readOnly
                value={invitation.acceptUrl}
                aria-label="Invitation link"
                className="font-mono text-xs"
                onFocus={(e) => e.target.select()}
              />
              <Button
                type="button"
                variant="outline"
                onClick={() =>
                  void navigator.clipboard
                    .writeText(invitation.acceptUrl)
                    .then(() => toast.success("Link copied"))
                }
              >
                <CopyIcon data-icon="inline-start" aria-hidden /> Copy
              </Button>
            </div>
            <DialogFooter>
              <Button onClick={() => close(false)}>Done</Button>
            </DialogFooter>
          </div>
        ) : (
          <form onSubmit={onSubmit} noValidate className="flex flex-col gap-5">
            <DialogHeader>
              <DialogTitle>Invite someone</DialogTitle>
              <DialogDescription>
                They choose their own name and password from the link.
              </DialogDescription>
            </DialogHeader>
            <FormAlert message={failure ? { title: failure.message } : null} />
            <FieldGroup>
              <Field data-invalid={Boolean(form.formState.errors.email)}>
                <FieldLabel htmlFor="invite-email">Email</FieldLabel>
                <Input id="invite-email" type="email" autoFocus {...form.register("email")} />
                <FieldError errors={[form.formState.errors.email]} />
              </Field>
              <Controller
                control={form.control}
                name="role"
                render={({ field }) => (
                  <Field>
                    <FieldLabel htmlFor="invite-role">Role</FieldLabel>
                    <Select value={field.value} onValueChange={field.onChange}>
                      <SelectTrigger id="invite-role" className="w-full">
                        <SelectValue />
                      </SelectTrigger>
                      <SelectContent>
                        {roles.map((r) => (
                          <SelectItem key={r} value={r}>
                            {roleLabel[r]}
                          </SelectItem>
                        ))}
                      </SelectContent>
                    </Select>
                    <p className="text-xs text-muted-foreground">{roleHelp[field.value]}</p>
                  </Field>
                )}
              />
            </FieldGroup>
            <DialogFooter>
              <Button type="button" variant="ghost" onClick={() => close(false)}>
                Cancel
              </Button>
              <Button type="submit" disabled={form.formState.isSubmitting}>
                Create invitation
              </Button>
            </DialogFooter>
          </form>
        )}
      </DialogContent>
    </Dialog>
  );
}

export function UsersAdmin() {
  const me = useCurrentUser();
  const format = useFormat();
  const queryClient = useQueryClient();
  const [inviting, setInviting] = useState(false);
  const users = useCursorList("/api/v1/users", { query: { limit: 50 } });
  const setRole = api.useMutation("put", "/api/v1/users/{userID}/role");
  const setStatus = api.useMutation("put", "/api/v1/users/{userID}/status");
  const revoke = api.useMutation("delete", "/api/v1/users/{userID}/sessions");
  const unlock = api.useMutation("post", "/api/v1/users/{userID}/unlock");

  const run = async (action: () => Promise<unknown>, success: string) => {
    try {
      await action();
      toast.success(success);
      await queryClient.invalidateQueries({ queryKey: ["get", "/api/v1/users"] });
    } catch (error) {
      if (!isApiError(error)) throw error;
      toast.error(error.message);
    }
  };

  return (
    <div className="flex flex-col gap-5">
      <div className="flex flex-wrap items-end justify-between gap-4">
        <div>
          <h1 className="text-xl font-semibold">Users</h1>
          <p className="text-sm text-muted-foreground">
            Everyone with an account. Qavia is invite only; a role change or a disable signs the
            person out at once.
          </p>
        </div>
        <Button onClick={() => setInviting(true)}>
          <UserPlusIcon data-icon="inline-start" aria-hidden /> Invite
        </Button>
      </div>

      {users.isLoading ? (
        <Skeleton className="h-40 w-full" />
      ) : users.error ? (
        <ErrorState error={users.error} onRetry={() => void users.refetch()} />
      ) : users.items.length === 0 ? (
        <EmptyState icon={UsersIcon} title="No one yet" description="Invite your team." />
      ) : (
        <div className="rounded-lg border bg-card">
          <Table>
            <TableHeader>
              <TableRow>
                <TableHead>Person</TableHead>
                <TableHead>Role</TableHead>
                <TableHead className="hidden md:table-cell">Last sign in</TableHead>
                <TableHead className="w-12">
                  <span className="sr-only">Actions</span>
                </TableHead>
              </TableRow>
            </TableHeader>
            <TableBody>
              {users.items.map((user) => {
                const self = user.id === me.id;
                return (
                  <TableRow key={user.id}>
                    <TableCell>
                      <p className="font-medium">
                        {user.name}
                        {self ? <span className="text-muted-foreground"> (you)</span> : null}
                        {user.disabled ? (
                          <Badge variant="destructive" className="ml-2">
                            Disabled
                          </Badge>
                        ) : null}
                      </p>
                      <p className="text-xs text-muted-foreground">{user.email}</p>
                    </TableCell>
                    <TableCell>{roleLabel[user.role]}</TableCell>
                    <TableCell className="hidden text-xs text-muted-foreground md:table-cell">
                      {user.lastLoginAt ? format.relative(user.lastLoginAt) : "Never"}
                    </TableCell>
                    <TableCell>
                      <DropdownMenu>
                        <DropdownMenuTrigger asChild>
                          <Button
                            variant="ghost"
                            size="icon-sm"
                            aria-label={`Actions for ${user.name}`}
                          >
                            <MoreHorizontalIcon aria-hidden />
                          </Button>
                        </DropdownMenuTrigger>
                        <DropdownMenuContent align="end" className="w-56">
                          <DropdownMenuLabel className="text-xs text-muted-foreground">
                            {self ? "Ask another admin to change your role" : "Role"}
                          </DropdownMenuLabel>
                          <DropdownMenuRadioGroup
                            value={user.role}
                            onValueChange={(role) =>
                              void run(
                                () =>
                                  setRole.mutateAsync({
                                    params: { path: { userID: user.id } },
                                    body: { role: role as Role },
                                  }),
                                `${user.name} is now ${roleLabel[role as Role]}`,
                              )
                            }
                          >
                            {roles.map((r) => (
                              <DropdownMenuRadioItem key={r} value={r} disabled={self}>
                                {roleLabel[r]}
                              </DropdownMenuRadioItem>
                            ))}
                          </DropdownMenuRadioGroup>
                          <DropdownMenuSeparator />
                          <DropdownMenuItem
                            disabled={self}
                            onSelect={() =>
                              void run(
                                () => revoke.mutateAsync({ params: { path: { userID: user.id } } }),
                                `${user.name} signed out everywhere`,
                              )
                            }
                          >
                            Sign out everywhere
                          </DropdownMenuItem>
                          <DropdownMenuItem
                            onSelect={() =>
                              void run(
                                () => unlock.mutateAsync({ params: { path: { userID: user.id } } }),
                                `Lockout cleared for ${user.name}`,
                              )
                            }
                          >
                            Clear sign-in lockout
                          </DropdownMenuItem>
                          <DropdownMenuItem
                            disabled={self}
                            variant={user.disabled ? "default" : "destructive"}
                            onSelect={() =>
                              void run(
                                () =>
                                  setStatus.mutateAsync({
                                    params: { path: { userID: user.id } },
                                    body: { disabled: !user.disabled },
                                  }),
                                user.disabled
                                  ? `${user.name} re-enabled`
                                  : `${user.name} disabled and signed out`,
                              )
                            }
                          >
                            {user.disabled ? "Re-enable account" : "Disable account"}
                          </DropdownMenuItem>
                        </DropdownMenuContent>
                      </DropdownMenu>
                    </TableCell>
                  </TableRow>
                );
              })}
            </TableBody>
          </Table>
        </div>
      )}
      {users.hasMore ? (
        <Button
          variant="outline"
          className="w-fit self-center"
          onClick={() => void users.loadMore()}
        >
          Load more
        </Button>
      ) : null}
      <InviteDialog open={inviting} onOpenChange={setInviting} />
    </div>
  );
}
