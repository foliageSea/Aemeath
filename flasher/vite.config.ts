import { defineConfig } from "vitest/config";

export default defineConfig({
  // Relative asset URLs so the site works under the GitHub Pages sub-path.
  base: "./",
  build: { target: "es2022" },
  test: { environment: "node", include: ["test/**/*.test.ts"] },
});
