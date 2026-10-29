import { defineConfig } from "vite";
import react from "@vitejs/plugin-react";

// Build output is embedded into the Go binary via go:embed.
// Use relative asset paths so the SPA works behind any tunnel host/path.
export default defineConfig({
  plugins: [react()],
  base: "./",
  build: {
    outDir: "../client/internal/webui/dist",
    emptyOutDir: true,
  },
  server: {
    port: 5173,
    proxy: {
      // During `npm run dev`, forward API/WS to a locally running client.
      "/api": "http://127.0.0.1:7777",
      "/ws": {
        target: "ws://127.0.0.1:7777",
        ws: true,
      },
    },
  },
});
