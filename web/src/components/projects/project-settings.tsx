"use client";

import { zodResolver } from "@hookform/resolvers/zod";
import { useQueryClient } from "@tanstack/react-query";
import { ArchiveIcon, ArchiveRestoreIcon, Trash2Icon, UserPlusIcon } from "lucide-react";
import { useRouter } from "next/navigation";
import { useState } from "react";
import { Controller, useForm } from "react-hook-form";
import { toast } from "sonner";
import { z } from "zod";

import { useCurrentUser } from "@/components/auth/current-user";
import { roleLabel } from "@/components/shell/user-menu";
import { SettingsForm } from "@/components/settings/settings-form";
import {
  AlertDialog,
  AlertDialogAction,
  AlertDialogCancel,
  AlertDialogContent,
  AlertDialogDescription,
  AlertDialogFooter,
  AlertDialogHeader,
  AlertDialogTitle,
  AlertDialogTrigger,
} from "@/components/ui/alert-dialog";
import { Badge } from "@/components/ui/badge";
import { Button } from "@/components/ui/button";
import { Checkbox } from "@/components/ui/checkbox";
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
import { Switch } from "@/components/ui/switch";
import { Tabs, TabsContent, TabsList, TabsTrigger } from "@/components/ui/tabs";
import { Textarea } from "@/components/ui/textarea";
import { api } from "@/lib/api/client";
import { isApiError } from "@/lib/api/errors";
import type { Role } from "@/lib/auth/roles";
import { hasRole } from "@/lib/auth/roles";
import { useCursorList } from "@/lib/hooks/use-cursor-list";

import { DisabledReason } from "./disabled-reason";
import { useCanMutate, useProject } from "./project-context";
import { allTestTypes, type TestType, testTypes } from "./test-types";

const roles: Role[] = ["viewer", "qa_engineer", "qa_lead", "admin"];

function Section({
  title,
  description,
  children,
}: {
  title: string;
  description?: string;
  children: React.ReactNode;
}) {
  return (
    <section className="rounded-lg border bg-card">
      <div className="border-b px-5 py-3">
        <h2 className="text-sm font-semibold">{title}</h2>
        {description ? <p className="text-xs text-muted-foreground">{description}</p> : null}
      </div>
      <div className="p-5">{children}</div>
    </section>
  );
}

const generalSchema = z.object({
  name: z.string().trim().min(1, "A project needs a name.").max(200),
  description: z.string().max(2000),
  testTypes: z.array(z.string()).min(1, "Keep at least one kind of testing."),
});

function General() {
  const project = useProject();
  const router = useRouter();
  const permission = useCanMutate("qa_engineer");
  const update = api.useMutation("patch", "/api/v1/projects/{projectID}");
  const form = useForm<z.infer<typeof generalSchema>>({
    resolver: zodResolver(generalSchema),
    values: {
      name: project.name,
      description: project.description,
      testTypes: project.testTypes.map((t) => t.type),
    },
  });

  const onSubmit = form.handleSubmit(async (values) => {
    try {
      await update.mutateAsync({
        params: { path: { projectID: project.id } },
        body: { ...values, testTypes: values.testTypes as TestType[] },
      });
      toast.success("Project updated");
      router.refresh();
    } catch (error) {
      if (!isApiError(error)) throw error;
      toast.error(error.message);
    }
  });

  return (
    <Section title="General">
      <form onSubmit={onSubmit} noValidate className="flex flex-col gap-5">
        <fieldset disabled={!permission.allowed} className="contents">
          <FieldGroup>
            <Field data-invalid={Boolean(form.formState.errors.name)}>
              <FieldLabel htmlFor="name">Name</FieldLabel>
              <Input id="name" {...form.register("name")} />
              <FieldError errors={[form.formState.errors.name]} />
            </Field>
            <Field>
              <FieldLabel htmlFor="description">Description</FieldLabel>
              <Textarea id="description" rows={2} {...form.register("description")} />
            </Field>
            <Controller
              control={form.control}
              name="testTypes"
              render={({ field, fieldState }) => (
                <Field data-invalid={Boolean(fieldState.error)}>
                  <FieldLabel asChild>
                    <span>Produces</span>
                  </FieldLabel>
                  <div className="grid gap-2 sm:grid-cols-2">
                    {allTestTypes.map((type) => (
                      <label key={type} className="flex items-center gap-2 text-sm">
                        <Checkbox
                          checked={field.value.includes(type)}
                          disabled={!permission.allowed}
                          onCheckedChange={(on) =>
                            field.onChange(
                              on === true
                                ? [...field.value, type]
                                : field.value.filter((t) => t !== type),
                            )
                          }
                        />
                        {testTypes[type].label}
                      </label>
                    ))}
                  </div>
                  <FieldError errors={[fieldState.error]} />
                </Field>
              )}
            />
          </FieldGroup>
        </fieldset>
        <div className="flex flex-col gap-1">
          <Button
            type="submit"
            className="w-fit"
            disabled={!permission.allowed || !form.formState.isDirty || form.formState.isSubmitting}
          >
            Save
          </Button>
          <DisabledReason permission={permission} />
        </div>
      </form>
    </Section>
  );
}

function ExternalAI() {
  const project = useProject();
  const router = useRouter();
  const user = useCurrentUser();
  const isAdmin = hasRole(user, "admin");
  const approve = api.useMutation("put", "/api/v1/projects/{projectID}/external-ai-approval");

  const toggle = async (approved: boolean) => {
    try {
      await approve.mutateAsync({
        params: { path: { projectID: project.id } },
        body: { approved },
      });
      toast.success(approved ? "External AI approved" : "Limited to local providers");
      router.refresh();
    } catch (error) {
      if (!isApiError(error)) throw error;
      toast.error(error.message);
    }
  };

  return (
    <Section
      title="Client data and external AI"
      description="Whether this project's specifications and results may be sent to an external model provider."
    >
      <div className="flex items-start gap-3">
        <Switch
          id="external-ai"
          checked={project.externalAiApproved}
          disabled={!isAdmin || project.archived || approve.isPending}
          onCheckedChange={(on) => void toggle(on)}
        />
        <div className="flex flex-col gap-1">
          <label htmlFor="external-ai" className="text-sm font-medium">
            Approved for external AI providers
          </label>
          <p className="text-xs text-muted-foreground">
            Off means only providers marked local may process this project, which is checked before
            any AI job is queued. Turn on only once the client has agreed to the subprocessors.
          </p>
          {!isAdmin ? (
            <p className="text-xs text-muted-foreground">Only an admin can change this.</p>
          ) : null}
        </div>
      </div>
    </Section>
  );
}

function Members() {
  const project = useProject();
  const user = useCurrentUser();
  const queryClient = useQueryClient();
  const permission = useCanMutate("qa_lead");
  const isAdmin = hasRole(user, "admin");
  const members = api.useQuery("get", "/api/v1/projects/{projectID}/members", {
    params: { path: { projectID: project.id } },
  });
  const users = useCursorList("/api/v1/users", { query: { limit: 100 } }, { enabled: isAdmin });
  const setRole = api.useMutation("put", "/api/v1/projects/{projectID}/members/{userID}");
  const remove = api.useMutation("delete", "/api/v1/projects/{projectID}/members/{userID}");
  const [adding, setAdding] = useState<string>("");
  const [addRole, setAddRole] = useState<Role>("qa_engineer");

  const refresh = () =>
    queryClient.invalidateQueries({ queryKey: ["get", "/api/v1/projects/{projectID}/members"] });

  const run = async (action: () => Promise<unknown>, success: string) => {
    try {
      await action();
      toast.success(success);
      await refresh();
    } catch (error) {
      if (!isApiError(error)) throw error;
      toast.error(error.message);
    }
  };

  const memberIDs = new Set(members.data?.items.map((m) => m.userId));
  const candidates = users.items.filter((u) => !memberIDs.has(u.id) && !u.disabled);

  return (
    <Section
      title="Members"
      description="A member's project role is separate from their platform role; the narrower of the two applies."
    >
      {members.isLoading ? (
        <Skeleton className="h-24 w-full" />
      ) : members.error ? (
        <ErrorState error={members.error} onRetry={() => void members.refetch()} />
      ) : (
        <div className="flex flex-col gap-4">
          <ul className="divide-y rounded-md border">
            {members.data!.items.map((member) => (
              <li key={member.userId} className="flex flex-wrap items-center gap-3 px-3 py-2">
                <div className="min-w-0 flex-1">
                  <p className="truncate text-sm font-medium">
                    {member.name}
                    {member.isOwner ? (
                      <Badge variant="secondary" className="ml-2">
                        Owner
                      </Badge>
                    ) : null}
                  </p>
                  <p className="truncate text-xs text-muted-foreground">{member.email}</p>
                </div>
                {member.isOwner ? (
                  <span className="text-xs text-muted-foreground">{roleLabel[member.role]}</span>
                ) : (
                  <>
                    <Select
                      value={member.role}
                      disabled={!permission.allowed || setRole.isPending}
                      onValueChange={(role) =>
                        void run(
                          () =>
                            setRole.mutateAsync({
                              params: { path: { projectID: project.id, userID: member.userId } },
                              body: { role: role as Role },
                            }),
                          `${member.name} is now ${roleLabel[role as Role]} on this project`,
                        )
                      }
                    >
                      <SelectTrigger
                        size="sm"
                        className="w-36"
                        aria-label={`Project role for ${member.name}`}
                      >
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
                    <Button
                      variant="ghost"
                      size="icon-sm"
                      aria-label={`Remove ${member.name}`}
                      disabled={!permission.allowed || remove.isPending}
                      onClick={() =>
                        void run(
                          () =>
                            remove.mutateAsync({
                              params: { path: { projectID: project.id, userID: member.userId } },
                            }),
                          `${member.name} removed`,
                        )
                      }
                    >
                      <Trash2Icon aria-hidden />
                    </Button>
                  </>
                )}
              </li>
            ))}
          </ul>

          {isAdmin ? (
            <div className="flex flex-wrap items-end gap-2">
              <div className="flex flex-col gap-1">
                <label htmlFor="add-member" className="text-xs text-muted-foreground">
                  Add someone
                </label>
                <Select value={adding} onValueChange={setAdding} disabled={!permission.allowed}>
                  <SelectTrigger id="add-member" className="w-64">
                    <SelectValue
                      placeholder={
                        candidates.length ? "Choose a person" : "Everyone is already a member"
                      }
                    />
                  </SelectTrigger>
                  <SelectContent>
                    {candidates.map((u) => (
                      <SelectItem key={u.id} value={u.id}>
                        {u.name} · {u.email}
                      </SelectItem>
                    ))}
                  </SelectContent>
                </Select>
              </div>
              <Select
                value={addRole}
                onValueChange={(r) => setAddRole(r as Role)}
                disabled={!permission.allowed}
              >
                <SelectTrigger className="w-36" aria-label="Project role for the new member">
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
              <Button
                disabled={!permission.allowed || !adding || setRole.isPending}
                onClick={() =>
                  void run(
                    () =>
                      setRole.mutateAsync({
                        params: { path: { projectID: project.id, userID: adding } },
                        body: { role: addRole },
                      }),
                    "Member added",
                  ).then(() => setAdding(""))
                }
              >
                <UserPlusIcon data-icon="inline-start" aria-hidden />
                Add
              </Button>
            </div>
          ) : (
            <p className="text-xs text-muted-foreground">
              Only an admin can look up accounts to add someone new. You can change roles and remove
              members.
            </p>
          )}
          <DisabledReason permission={permission} />
        </div>
      )}
    </Section>
  );
}

function Archive() {
  const project = useProject();
  const router = useRouter();
  const user = useCurrentUser();
  const allowed = hasRole(user, "qa_lead");
  const archive = api.useMutation("post", "/api/v1/projects/{projectID}/archive");
  const unarchive = api.useMutation("post", "/api/v1/projects/{projectID}/unarchive");

  const act = async () => {
    try {
      const params = { params: { path: { projectID: project.id } } };
      if (project.archived) await unarchive.mutateAsync(params);
      else await archive.mutateAsync(params);
      toast.success(project.archived ? "Project unarchived" : "Project archived");
      router.refresh();
    } catch (error) {
      if (!isApiError(error)) throw error;
      toast.error(error.message);
    }
  };

  return (
    <Section
      title={project.archived ? "Unarchive" : "Archive"}
      description={
        project.archived
          ? "Make the project editable again."
          : "An archived project keeps everything, but nothing in it can be changed or run."
      }
    >
      <AlertDialog>
        <AlertDialogTrigger asChild>
          <Button variant={project.archived ? "outline" : "destructive"} disabled={!allowed}>
            {project.archived ? (
              <ArchiveRestoreIcon data-icon="inline-start" aria-hidden />
            ) : (
              <ArchiveIcon data-icon="inline-start" aria-hidden />
            )}
            {project.archived ? "Unarchive project" : "Archive project"}
          </Button>
        </AlertDialogTrigger>
        <AlertDialogContent>
          <AlertDialogHeader>
            <AlertDialogTitle>
              {project.archived ? `Unarchive ${project.name}?` : `Archive ${project.name}?`}
            </AlertDialogTitle>
            <AlertDialogDescription>
              {project.archived
                ? "Members will be able to upload, generate, and run again."
                : "Nothing is deleted. Uploads, generation, runs, and edits stop until it is unarchived. Scheduled runs for it are skipped."}
            </AlertDialogDescription>
          </AlertDialogHeader>
          <AlertDialogFooter>
            <AlertDialogCancel>Keep as is</AlertDialogCancel>
            <AlertDialogAction onClick={() => void act()}>
              {project.archived ? "Unarchive" : "Archive"}
            </AlertDialogAction>
          </AlertDialogFooter>
        </AlertDialogContent>
      </AlertDialog>
      {!allowed ? (
        <p className="mt-2 text-xs text-muted-foreground">
          Only a QA Lead or an admin can do this.
        </p>
      ) : null}
    </Section>
  );
}

export function ProjectSettings() {
  const project = useProject();
  const permission = useCanMutate("qa_engineer");
  return (
    <Tabs defaultValue="general" className="max-w-5xl">
      <TabsList>
        <TabsTrigger value="general">General</TabsTrigger>
        <TabsTrigger value="members">Members</TabsTrigger>
        <TabsTrigger value="overrides">Overrides</TabsTrigger>
      </TabsList>
      <TabsContent value="general" className="flex max-w-3xl flex-col gap-6 pt-4">
        <General />
        <ExternalAI />
        <Archive />
      </TabsContent>
      <TabsContent value="members" className="max-w-3xl pt-4">
        <Members />
      </TabsContent>
      <TabsContent value="overrides" className="flex flex-col gap-3 pt-4">
        <p className="text-sm text-muted-foreground">
          Platform settings this project overrides. Anything not set here inherits the platform
          default.
        </p>
        <SettingsForm
          scope="project"
          projectID={project.id}
          readOnlyReason={permission.allowed ? undefined : permission.reason}
        />
      </TabsContent>
    </Tabs>
  );
}
