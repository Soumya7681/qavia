"use client";

import { zodResolver } from "@hookform/resolvers/zod";
import { useQueryClient } from "@tanstack/react-query";
import { useRouter } from "next/navigation";
import { useState } from "react";
import { Controller, useForm } from "react-hook-form";
import { z } from "zod";

import { FormAlert } from "@/components/auth/form-alert";
import { Button } from "@/components/ui/button";
import { Checkbox } from "@/components/ui/checkbox";
import {
  Dialog,
  DialogContent,
  DialogDescription,
  DialogFooter,
  DialogHeader,
  DialogTitle,
} from "@/components/ui/dialog";
import {
  Field,
  FieldDescription,
  FieldError,
  FieldGroup,
  FieldLabel,
  FieldLegend,
  FieldSet,
} from "@/components/ui/field";
import { Input } from "@/components/ui/input";
import { Textarea } from "@/components/ui/textarea";
import { api } from "@/lib/api/client";
import { type ApiError, isApiError } from "@/lib/api/errors";

import { allTestTypes, type TestType, testTypes } from "./test-types";

const schema = z.object({
  name: z.string().trim().min(1, "Give the project a name.").max(200),
  description: z.string().max(2000),
  testTypes: z.array(z.string()).min(1, "Pick at least one kind of testing."),
});
type Values = z.infer<typeof schema>;

export function CreateProjectDialog({
  open,
  onOpenChange,
}: {
  open: boolean;
  onOpenChange: (open: boolean) => void;
}) {
  const router = useRouter();
  const queryClient = useQueryClient();
  const [failure, setFailure] = useState<ApiError | null>(null);
  const form = useForm<Values>({
    resolver: zodResolver(schema),
    defaultValues: { name: "", description: "", testTypes: ["test_cases", "api_tests"] },
  });
  const create = api.useMutation("post", "/api/v1/projects");

  const onSubmit = form.handleSubmit(async (values) => {
    setFailure(null);
    try {
      const project = await create.mutateAsync({
        body: { ...values, testTypes: values.testTypes as TestType[] },
      });
      await queryClient.invalidateQueries({ queryKey: ["get", "/api/v1/projects"] });
      onOpenChange(false);
      form.reset();
      router.push(`/projects/${project.id}`);
    } catch (error) {
      if (!isApiError(error)) throw error;
      setFailure(error);
    }
  });

  return (
    <Dialog open={open} onOpenChange={onOpenChange}>
      <DialogContent className="sm:max-w-xl">
        <form onSubmit={onSubmit} noValidate className="flex flex-col gap-5">
          <DialogHeader>
            <DialogTitle>New project</DialogTitle>
            <DialogDescription>
              A project holds one system under test: its specification, its cases, and its runs.
            </DialogDescription>
          </DialogHeader>
          <FormAlert message={failure ? { title: failure.message } : null} />
          <FieldGroup>
            <Field data-invalid={Boolean(form.formState.errors.name)}>
              <FieldLabel htmlFor="project-name">Name</FieldLabel>
              <Input
                id="project-name"
                autoFocus
                placeholder="Orders API"
                {...form.register("name")}
              />
              <FieldError errors={[form.formState.errors.name]} />
            </Field>
            <Field>
              <FieldLabel htmlFor="project-description">
                Description <span className="font-normal text-muted-foreground">optional</span>
              </FieldLabel>
              <Textarea id="project-description" rows={2} {...form.register("description")} />
            </Field>
            <Controller
              control={form.control}
              name="testTypes"
              render={({ field, fieldState }) => (
                <FieldSet data-invalid={Boolean(fieldState.error)}>
                  <FieldLegend variant="label">What should Qavia produce?</FieldLegend>
                  <FieldDescription>
                    You can change this later in the project&apos;s settings.
                  </FieldDescription>
                  <div className="grid gap-2 sm:grid-cols-2">
                    {allTestTypes.map((type) => {
                      const id = `type-${type}`;
                      const checked = field.value.includes(type);
                      return (
                        <Field
                          key={type}
                          orientation="horizontal"
                          className="items-start rounded-md border p-3"
                        >
                          <Checkbox
                            id={id}
                            checked={checked}
                            onCheckedChange={(on) =>
                              field.onChange(
                                on === true
                                  ? [...field.value, type]
                                  : field.value.filter((t) => t !== type),
                              )
                            }
                          />
                          <div className="flex flex-col gap-0.5">
                            <FieldLabel htmlFor={id}>{testTypes[type].label}</FieldLabel>
                            <span className="text-xs text-muted-foreground">
                              {testTypes[type].description}
                            </span>
                          </div>
                        </Field>
                      );
                    })}
                  </div>
                  <FieldError errors={[fieldState.error]} />
                </FieldSet>
              )}
            />
          </FieldGroup>
          <DialogFooter>
            <Button type="button" variant="ghost" onClick={() => onOpenChange(false)}>
              Cancel
            </Button>
            <Button type="submit" disabled={form.formState.isSubmitting}>
              {form.formState.isSubmitting ? "Creating…" : "Create project"}
            </Button>
          </DialogFooter>
        </form>
      </DialogContent>
    </Dialog>
  );
}
