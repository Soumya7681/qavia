import type { components } from "@/lib/api/schema";

export type TestType = components["schemas"]["TestType"];

/**
 * Every test type, in the order a form shows them. `satisfies` keeps this list
 * exhaustive against the generated union: a type added to qavia.yaml fails the
 * build here until it has a label.
 */
export const testTypes = {
  test_cases: {
    label: "Test cases",
    description: "Readable cases generated from requirements and the spec.",
  },
  api_tests: { label: "API tests", description: "Runnable API tests and a Postman collection." },
  unit_tests: { label: "Unit tests", description: "Unit tests for a connected repository's code." },
  ui_tests: { label: "UI tests", description: "Browser tests discovered from the running app." },
  performance_tests: {
    label: "Performance",
    description: "Load tests against an authorised target.",
  },
  security_tests: {
    label: "Security",
    description: "Probes for common vulnerabilities, with approval.",
  },
  test_data: { label: "Test data", description: "Seeded, reproducible data from your schemas." },
  mock_server: { label: "Mock server", description: "A mock of your API for client development." },
} as const satisfies Record<TestType, { label: string; description: string }>;

export const allTestTypes = Object.keys(testTypes) as TestType[];
