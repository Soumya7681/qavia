import type { components } from "@/lib/api/schema";

export type ArtifactKind = components["schemas"]["ArtifactKind"];

/**
 * What each input kind is, which files look like it, and whether submitting it
 * starts test generation. The extensions are a quick client-side check only: the
 * server sniffs the content and is the authority (FE-0.6).
 */
export const artifactKinds = {
  openapi: {
    label: "OpenAPI specification",
    extensions: [".yaml", ".yml", ".json"],
    processes: true,
    help: "OpenAPI 3.x or Swagger 2, YAML or JSON.",
  },
  postman: {
    label: "Postman collection",
    extensions: [".json"],
    processes: true,
    help: "A Postman v2.1 collection export.",
  },
  requirement_text: {
    label: "Requirements document",
    extensions: [".txt", ".md", ".markdown"],
    processes: true,
    help: "Plain text or Markdown user stories and acceptance criteria.",
  },
  document: {
    label: "Document",
    extensions: [".pdf", ".docx"],
    processes: true,
    help: "A PDF or Word document describing the system.",
  },
  source_archive: {
    label: "Source archive",
    extensions: [".zip", ".tar.gz", ".tgz"],
    processes: false,
    help: "Repository source for unit tests and coverage, if you cannot connect the repository.",
  },
  sql_dump: {
    label: "SQL dump",
    extensions: [".sql"],
    processes: false,
    help: "A schema dump, used to generate realistic test data.",
  },
} as const satisfies Record<
  ArtifactKind,
  { label: string; extensions: readonly string[]; processes: boolean; help: string }
>;

export const allArtifactKinds = Object.keys(artifactKinds) as ArtifactKind[];

/** The kind a file most likely is, from its name. Only a starting suggestion. */
export function guessKind(filename: string): ArtifactKind | undefined {
  const name = filename.toLowerCase();
  if (/\.(ya?ml)$/.test(name) || /openapi|swagger/.test(name)) return "openapi";
  if (/postman/.test(name) && name.endsWith(".json")) return "postman";
  if (name.endsWith(".json")) return "openapi";
  for (const kind of allArtifactKinds) {
    if (artifactKinds[kind].extensions.some((ext) => name.endsWith(ext))) return kind;
  }
  return undefined;
}

export function extensionMatches(kind: ArtifactKind, filename: string): boolean {
  const name = filename.toLowerCase();
  return artifactKinds[kind].extensions.some((ext) => name.endsWith(ext));
}
