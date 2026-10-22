import { defineConfig } from "vite";
import vue from "@vitejs/plugin-vue";
import { resolve } from "path";

export default defineConfig({
  plugins: [vue()],
  resolve: {
    alias: {
      "@": resolve(__dirname, "src"),
    },
  },
  build: {
    sourcemap: process.env.NODE_ENV !== "production",
    rollupOptions: {
      output: {
        manualChunks(id) {
          if (!id.includes("node_modules")) return;
          if (id.includes("highlight.js") || id.includes("marked")) {
            return "markdown";
          }
          if (id.includes("/vue/") || id.includes("/pinia/")) {
            return "vue";
          }
          return "vendor";
        },
      },
    },
  },
  server: {
    port: 5173,
    proxy: {
      // Overridable so the API can run on another port when 8080 is taken by
      // something else on the same machine.
      "/api": process.env.ODA_API_TARGET || "http://localhost:8080",
    },
  },
});
