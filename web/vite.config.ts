import react from "@vitejs/plugin-react";
import { defineConfig } from "vitest/config";

// `npm run dev` serves the app on :5173 and passes API calls to the API on
// :8080 (`make dev`). The browser sees one origin, so the session cookie and
// the API's same-origin check work as they do in production, where the API
// serves the built app itself (WEB_DIR).
export default defineConfig({
  plugins: [react()],
  server: {
    port: 5173,
    strictPort: true,
    proxy: {
      "/v1": "http://localhost:8080",
    },
  },
  build: {
    outDir: "dist",
    sourcemap: true,
  },
  test: {
    environment: "jsdom",
    include: ["src/**/*.test.{ts,tsx}"],
    restoreMocks: true,
  },
});
