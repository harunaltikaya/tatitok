import { defineConfig } from "vite";
import react from "@vitejs/plugin-react";
import tailwindcss from "@tailwindcss/vite";

// Dev-only proxy to a locally running `tatitok serve`; the production
// bundle is embedded in the Go binary and served same-origin, so the
// app always fetches relative /api/v1 paths — no external origins.
export default defineConfig({
  plugins: [react(), tailwindcss()],
  server: {
    proxy: {
      "/api": "http://127.0.0.1:8284",
    },
  },
  build: {
    // echarts is large but embedded by design (no runtime network).
    chunkSizeWarningLimit: 1600,
  },
});
