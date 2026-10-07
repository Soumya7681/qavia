"use client";

import { useQueryClient } from "@tanstack/react-query";
import { CheckCircle2Icon, CopyIcon, FileUpIcon, UploadIcon, XIcon } from "lucide-react";
import Link from "next/link";
import { useRef, useState } from "react";

import { FormAlert } from "@/components/auth/form-alert";
import { Button } from "@/components/ui/button";
import { Progress } from "@/components/ui/progress";
import {
  Select,
  SelectContent,
  SelectItem,
  SelectTrigger,
  SelectValue,
} from "@/components/ui/select";
import { isApiError } from "@/lib/api/errors";
import type { components } from "@/lib/api/schema";
import { uploadArtifact } from "@/lib/api/upload";
import { formatBytes } from "@/lib/format";
import { cn } from "@/lib/utils";

import {
  allArtifactKinds,
  type ArtifactKind,
  artifactKinds,
  extensionMatches,
  guessKind,
} from "./artifact-kinds";
import { DisabledReason } from "./disabled-reason";
import { useCanMutate, useProject } from "./project-context";
import { uploadRejection } from "./upload-messages";

type Artifact = components["schemas"]["Artifact"];

type State =
  | { phase: "idle" }
  | { phase: "uploading"; progress: number; controller: AbortController }
  | { phase: "done"; artifact: Artifact }
  | { phase: "failed"; title: string; description?: string };

export function UploadPanel({ onUploaded }: { onUploaded: (artifact: Artifact) => void }) {
  const project = useProject();
  const permission = useCanMutate("qa_engineer");
  const queryClient = useQueryClient();
  const input = useRef<HTMLInputElement>(null);
  const [file, setFile] = useState<File | null>(null);
  const [kind, setKind] = useState<ArtifactKind>("openapi");
  const [state, setState] = useState<State>({ phase: "idle" });
  const [dragging, setDragging] = useState(false);

  const choose = (next: File | undefined) => {
    if (!next) return;
    setFile(next);
    setKind(guessKind(next.name) ?? kind);
    setState({ phase: "idle" });
  };

  const upload = async () => {
    if (!file) return;
    const controller = new AbortController();
    setState({ phase: "uploading", progress: 0, controller });
    try {
      const artifact = await uploadArtifact(project.id, kind, file, {
        signal: controller.signal,
        onProgress: (progress) =>
          setState((s) => (s.phase === "uploading" ? { ...s, progress } : s)),
      });
      setState({ phase: "done", artifact });
      await queryClient.invalidateQueries({
        queryKey: ["get", "/api/v1/projects/{projectID}/artifacts"],
      });
      onUploaded(artifact);
    } catch (error) {
      if (error instanceof DOMException && error.name === "AbortError") {
        setState({ phase: "idle" });
        return;
      }
      if (!isApiError(error)) throw error;
      setState({ phase: "failed", ...uploadRejection(error, file) });
    }
  };

  const mismatch = file && !extensionMatches(kind, file.name);
  const busy = state.phase === "uploading";

  return (
    <div className="flex flex-col gap-4">
      <div
        onDragOver={(e) => {
          if (!permission.allowed) return;
          e.preventDefault();
          setDragging(true);
        }}
        onDragLeave={() => setDragging(false)}
        onDrop={(e) => {
          e.preventDefault();
          setDragging(false);
          if (permission.allowed) choose(e.dataTransfer.files[0]);
        }}
        className={cn(
          "flex flex-col items-center gap-2 rounded-lg border border-dashed px-6 py-8 text-center transition-colors",
          dragging && "border-primary bg-accent",
          !permission.allowed && "opacity-60",
        )}
      >
        <FileUpIcon aria-hidden className="size-6 text-muted-foreground" />
        <p className="text-sm">
          Drop a specification here, or{" "}
          <Button
            type="button"
            variant="link"
            className="h-auto p-0"
            disabled={!permission.allowed || busy}
            onClick={() => input.current?.click()}
          >
            choose a file
          </Button>
        </p>
        <p className="text-xs text-muted-foreground">
          OpenAPI, Postman, requirements, a document, source, or a SQL dump.
        </p>
        <input
          ref={input}
          type="file"
          className="sr-only"
          tabIndex={-1}
          aria-hidden
          onChange={(e) => choose(e.target.files?.[0])}
        />
      </div>
      <DisabledReason permission={permission} />

      {file ? (
        <div className="flex flex-col gap-3 rounded-lg border bg-card p-4">
          <div className="flex flex-wrap items-center gap-3">
            <div className="min-w-0 flex-1">
              <p className="truncate text-sm font-medium">{file.name}</p>
              <p className="text-xs text-muted-foreground">{formatBytes(file.size)}</p>
            </div>
            <Select value={kind} onValueChange={(k) => setKind(k as ArtifactKind)} disabled={busy}>
              <SelectTrigger className="w-60" aria-label="What this file is">
                <SelectValue />
              </SelectTrigger>
              <SelectContent>
                {allArtifactKinds.map((k) => (
                  <SelectItem key={k} value={k}>
                    {artifactKinds[k].label}
                  </SelectItem>
                ))}
              </SelectContent>
            </Select>
            {busy ? (
              <Button variant="ghost" onClick={() => state.controller.abort()}>
                <XIcon data-icon="inline-start" aria-hidden /> Cancel
              </Button>
            ) : (
              <Button onClick={() => void upload()} disabled={!permission.allowed}>
                <UploadIcon data-icon="inline-start" aria-hidden /> Upload
              </Button>
            )}
          </div>
          <p className="text-xs text-muted-foreground">
            {artifactKinds[kind].help}
            {mismatch ? (
              <span className="text-warning">
                {" "}
                The name does not look like this kind; the server checks the content and will say if
                it is wrong.
              </span>
            ) : null}
          </p>
          {busy ? (
            <div className="flex items-center gap-3" aria-live="polite">
              <Progress
                value={Math.round(state.progress * 100)}
                className="flex-1"
                aria-label="Upload progress"
              />
              <span className="w-10 text-right text-xs tabular-nums">
                {Math.round(state.progress * 100)}%
              </span>
            </div>
          ) : null}
          {state.phase === "failed" ? <FormAlert message={state} /> : null}
          {state.phase === "done" ? <UploadResult artifact={state.artifact} /> : null}
        </div>
      ) : null}
    </div>
  );
}

function UploadResult({ artifact }: { artifact: Artifact }) {
  if (artifact.deduplicated) {
    return (
      <div
        role="status"
        className="flex items-start gap-2 rounded-md bg-info-wash p-3 text-sm text-info"
      >
        <CopyIcon aria-hidden className="mt-0.5 size-4" />
        <p>
          An identical file is already uploaded (version {artifact.version}), so nothing new was
          stored and generation was not re-run.
        </p>
      </div>
    );
  }
  return (
    <div
      role="status"
      className="flex items-start gap-2 rounded-md bg-success-wash p-3 text-sm text-success"
    >
      <CheckCircle2Icon aria-hidden className="mt-0.5 size-4" />
      <p>
        Uploaded as version {artifact.version}.
        {artifact.version > 1 ? (
          <>
            {" "}
            Earlier versions are kept; see{" "}
            <Link href="#inputs" className="underline">
              the list below
            </Link>
            .
          </>
        ) : null}
      </p>
    </div>
  );
}
