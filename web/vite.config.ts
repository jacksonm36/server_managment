import { defineConfig } from "vite";
import react from "@vitejs/plugin-react";
import tailwindcss from "@tailwindcss/vite";
import path from "node:path";

export default defineConfig({
  plugins: [react(), tailwindcss()],
  resolve: {
    alias: { "@": path.resolve(__dirname, "src") },
  },
  server: {
    host: true,
    proxy: {
      "/api": "http://127.0.0.1:8090",
      "/install.sh": "http://127.0.0.1:8090",
    },
  },
  build: {
    target: "es2022",
    cssMinify: true,
    rollupOptions: {
      output: {
        manualChunks: { react: ["react", "react-dom"] },
      },
    },
  },
});
