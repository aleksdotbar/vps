import tailwindcss from "@tailwindcss/vite";
import react from "@vitejs/plugin-react";
import { defineConfig } from "vite";
import { readFileSync } from "node:fs";
import { isAbsolute } from "node:path";
import { fileURLToPath } from "node:url";

function developmentTLS() {
  const certificate = process.env.PANEL_DEV_TLS_CERT_FILE;
  const key = process.env.PANEL_DEV_TLS_KEY_FILE;
  if (!certificate || !key || !isAbsolute(certificate) || !isAbsolute(key)) {
    throw new Error(
      "Set PANEL_DEV_TLS_CERT_FILE and PANEL_DEV_TLS_KEY_FILE to absolute paths outside the repository for a trusted localhost certificate and key.",
    );
  }
  return { cert: readFileSync(certificate), key: readFileSync(key) };
}

export default defineConfig(({ command, mode }) => ({
  publicDir: false,
  plugins: [
    react({ babel: { plugins: ["babel-plugin-react-compiler"] } }),
    tailwindcss(),
  ],
  resolve: { dedupe: ["react", "react-dom", "@bufbuild/protobuf"] },
  server: {
    host: "127.0.0.1",
    port: 5173,
    https:
      command === "serve" && mode !== "mock" ? developmentTLS() : undefined,
    strictPort: true,
    fs: {
      allow: [
        fileURLToPath(new URL(".", import.meta.url)),
        fileURLToPath(new URL("../gen/ts", import.meta.url)),
      ],
    },
    proxy: {
      "/panel.v1.": { target: "http://127.0.0.1:8080", changeOrigin: false },
      "/github/manifest": {
        target: "http://127.0.0.1:8080",
        changeOrigin: false,
      },
      "/github/installation": {
        target: "http://127.0.0.1:8080",
        changeOrigin: false,
      },
    },
  },
  build: {
    target: "es2022",
    sourcemap: false,
    outDir: "../internal/webassets/dist",
    emptyOutDir: true,
  },
}));
