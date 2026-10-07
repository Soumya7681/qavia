import type { components } from "@/lib/api/schema";

type JobChain = components["schemas"]["JobChain"];

/** What a person calls each stage. An unknown type still reads sensibly. */
const stageLabels: Record<string, string> = {
  "noop.prepare": "Preparing",
  "noop.work": "Working",
  "noop.finalise": "Finalising",
  "ai.smoke": "Checking the AI provider",
  "ingest.parse": "Reading the specification",
  "ingest.extract": "Extracting requirements",
  "generate.design": "Designing test cases",
  "generate.code": "Writing test code",
  "execute.run": "Running tests",
  "analyse.run": "Analysing failures",
  "report.build": "Building the report",
  "repo.sync": "Syncing the repository",
  "repo.comprehend": "Mapping the code",
  "repo.unittests": "Writing unit tests",
  "coverage.measure": "Measuring coverage",
  "ui.discover": "Exploring the app",
  "ui.generate": "Writing UI tests",
  "mock.start": "Starting the mock server",
  "mock.stop": "Stopping the mock server",
  "perf.run": "Running the load test",
  "security.scan": "Scanning for vulnerabilities",
};

export function stageLabel(type: string): string {
  if (stageLabels[type]) return stageLabels[type];
  const last = type.split(".").at(-1) ?? type;
  return last.charAt(0).toUpperCase() + last.slice(1).replace(/_/g, " ");
}

export const chainLabels: Record<JobChain, string> = {
  noop: "Pipeline check",
  ai_smoke: "AI check",
  ingest: "Processing the specification",
  generate: "Test generation",
  execute: "Test run",
  analyse: "Failure analysis",
};

/** Where a finished chain's result lives in the project. */
export function resultLink(
  projectID: string,
  chain: JobChain | undefined,
): { href: string; label: string } | undefined {
  switch (chain) {
    case "ingest":
      return {
        href: `/projects/${projectID}/requirements`,
        label: "Review the extracted requirements",
      };
    case "generate":
      return {
        href: `/projects/${projectID}/test-cases`,
        label: "Review the generated test cases",
      };
    case "execute":
      return { href: `/projects/${projectID}/runs`, label: "See the run" };
    case "analyse":
      return { href: `/projects/${projectID}/defects`, label: "See the analysis" };
    default:
      return undefined;
  }
}
