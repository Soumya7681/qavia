import { fileURLToPath } from "node:url";

import react from "@vitejs/plugin-react";
import { defineConfig } from "vitest/config";

export default defineConfig({
  plugins: [react()],
  resolve: {
    alias: { "@": fileURLToPath(new URL("./src", import.meta.url)) },
  },
  test: {
    include: ["src/**/*.test.{ts,tsx}"],
    // Node by default; a component or hook test opts into jsdom with a
    // `@vitest-environment jsdom` comment, so pure logic stays fast.
    environment: "node",
    setupFiles: ["./src/test/setup.ts"],
    restoreMocks: true,
  },
});
