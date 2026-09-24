import { defineConfig } from "vite";
import react from "@vitejs/plugin-react";
import tailwindcss from "@tailwindcss/vite";

// The production bundle is written into the Go package that embeds it.
// `npm run dev` proxies API calls to a locally running server (make run).
export default defineConfig({
  plugins: [react(), tailwindcss()],
  build: {
    outDir: "../internal/api/ui/dist",
    emptyOutDir: true,
  },
  server: {
    proxy: {
      "/api": "http://localhost:8080",
      "/ready": "http://localhost:8080",
      "/metrics": "http://localhost:8080",
    },
  },
});
