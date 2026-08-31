// Copies KaTeX runtime assets from node_modules into the same-origin public
// assets directory. Report previews load chart and math runtimes from
// /assets/** only — no third-party CDN — so rendering works offline and the
// CSP can stay strict.
import { cp, mkdir, access } from "node:fs/promises";
import { constants } from "node:fs";
import { fileURLToPath } from "node:url";
import path from "node:path";

const root = path.dirname(path.dirname(fileURLToPath(import.meta.url)));
const source = path.join(root, "node_modules", "katex", "dist");
const target = path.join(root, "public", "assets", "katex");

async function exists(p) {
  try {
    await access(p, constants.F_OK);
    return true;
  } catch {
    return false;
  }
}

if (!(await exists(source))) {
  console.error(`katex dist not found at ${source}; run npm ci first`);
  process.exit(1);
}

await mkdir(target, { recursive: true });
for (const item of ["katex.min.css", "katex.min.js", "fonts", path.join("contrib", "auto-render.min.js")]) {
  await cp(path.join(source, item), path.join(target, item), { recursive: true });
}
console.log("copied katex assets to public/assets/katex");
