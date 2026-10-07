import { defineConfig, globalIgnores } from "eslint/config";
import nextVitals from "eslint-config-next/core-web-vitals";
import nextTs from "eslint-config-next/typescript";
import prettier from "eslint-config-prettier/flat";

const eslintConfig = defineConfig([
  ...nextVitals,
  ...nextTs,
  // Formatting is Prettier's job; this turns off the rules that would fight it.
  prettier,
  // Every call to the API goes through the generated client, so request and
  // response types cannot drift from qavia.yaml (work-frontend.md rule 1).
  {
    files: ["src/**/*.{ts,tsx}"],
    ignores: ["src/lib/api/**", "**/*.test.{ts,tsx}"],
    rules: {
      "no-restricted-globals": [
        "error",
        {
          name: "fetch",
          message: "Use the generated client in @/lib/api/client instead of fetch.",
        },
      ],
      "no-restricted-properties": [
        "error",
        {
          object: "globalThis",
          property: "fetch",
          message: "Use the generated client in @/lib/api/client.",
        },
        {
          object: "window",
          property: "fetch",
          message: "Use the generated client in @/lib/api/client.",
        },
      ],
    },
  },
  // Override default ignores of eslint-config-next.
  globalIgnores([
    // Default ignores of eslint-config-next:
    ".next/**",
    "out/**",
    "build/**",
    "next-env.d.ts",
    // Generated from qavia.yaml by `make gen-web`.
    "src/lib/api/schema.d.ts",
  ]),
]);

export default eslintConfig;
