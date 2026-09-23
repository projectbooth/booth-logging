/// <reference types="vitest/config" />
import { fileURLToPath } from "node:url";
import { defineConfig } from "vite";
import react from "@vitejs/plugin-react";

// Two personalities, one config (ADR 0030) — the same arrangement as booth-module-store:
//   - `vite`/`vite dev` (command "serve"): the dev harness — index.html/src/main.tsx wraps
//     LoggingApp in a mock shell (src/devshell/DevShell.tsx), since booth-design's real
//     shell doesn't run here.
//   - `vite build` (command "build"): builds the publishable library
//     (@projectbooth/logging-ui) from src/index.ts, external-izing react/react-dom so
//     booth-design's own copies are used (two React copies in one page is a classic cause
//     of "Invalid hook call").
export default defineConfig(({ command }) => ({
  plugins: [react()],
  build:
    command === "build"
      ? {
          lib: {
            entry: fileURLToPath(new URL("./src/index.ts", import.meta.url)),
            formats: ["es"],
            fileName: "index",
          },
          rollupOptions: {
            external: ["react", "react-dom", "react/jsx-runtime"],
          },
        }
      : undefined,
  server: {
    proxy: {
      // Local dev only: booth-core's gateway would normally proxy /modules/logging/* to
      // this service's own /api/* routes in a real deployment. Pointed at
      // BOOTH_LOGGING_DEV_BACKEND when set, so `npm run dev` can hit a locally running Go
      // backend without CORS juggling.
      "/modules/logging": {
        target: process.env.BOOTH_LOGGING_DEV_BACKEND ?? "http://localhost:8080",
        changeOrigin: true,
        // Mimics the gateway's prefix-stripping: /modules/logging/api/backends reaches
        // the backend as /api/backends.
        rewrite: (path) => path.replace(/^\/modules\/logging/, ""),
      },
    },
  },
  test: {
    environment: "jsdom",
    globals: true,
    setupFiles: ["./src/setupTests.ts"],
  },
}));
