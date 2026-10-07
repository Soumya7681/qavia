"use client";

import { CheckCircle2Icon, CircleDashedIcon } from "lucide-react";

import { useFormat } from "@/lib/hooks/use-format";

import { useProject } from "./project-context";
import { testTypes } from "./test-types";

/** The project's identity and what it produces. The live dashboard joins this in FE-2.6. */
export function ProjectOverview() {
  const project = useProject();
  const format = useFormat();
  return (
    <div className="grid gap-6 lg:grid-cols-[minmax(0,2fr)_minmax(0,1fr)]">
      <section className="rounded-lg border bg-card p-5" aria-labelledby="produces">
        <h2 id="produces" className="text-sm font-semibold">
          What this project produces
        </h2>
        <ul className="mt-3 grid gap-2 sm:grid-cols-2">
          {project.testTypes.map((selection) => (
            <li key={selection.type} className="flex items-start gap-2 text-sm">
              {selection.available ? (
                <CheckCircle2Icon aria-hidden className="mt-0.5 size-4 text-success" />
              ) : (
                <CircleDashedIcon aria-hidden className="mt-0.5 size-4 text-muted-foreground" />
              )}
              <span>
                {testTypes[selection.type]?.label ?? selection.type}
                {!selection.available && selection.reason ? (
                  <span className="block text-xs text-muted-foreground">{selection.reason}</span>
                ) : null}
              </span>
            </li>
          ))}
        </ul>
      </section>
      <section className="rounded-lg border bg-card p-5 text-sm" aria-labelledby="details">
        <h2 id="details" className="font-semibold">
          Details
        </h2>
        <dl className="mt-3 grid grid-cols-[7rem_1fr] gap-y-2">
          <dt className="text-muted-foreground">Created</dt>
          <dd>
            <time dateTime={project.createdAt}>{format.dateTime(project.createdAt)}</time>
          </dd>
          <dt className="text-muted-foreground">External AI</dt>
          <dd>{project.externalAiApproved ? "Approved" : "Local providers only"}</dd>
          <dt className="text-muted-foreground">Status</dt>
          <dd>{project.archived ? "Archived" : "Active"}</dd>
        </dl>
      </section>
    </div>
  );
}
