import path from "node:path";
import tailwindcss from "@tailwindcss/vite";
import vue from "@vitejs/plugin-vue";
import { defineConfig } from "vitest/config";

export default defineConfig({
  // Relative asset URLs so the site works under the GitHub Pages sub-path.
  base: "./",
  plugins: [vue(), tailwindcss()],
  resolve: { alias: { "@": path.resolve(import.meta.dirname, "./src") } },
  build: { target: "es2022" },
  test: { environment: "node", include: ["test/**/*.test.ts"] },
});
