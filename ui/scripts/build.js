import * as esbuild from "esbuild";
import vuePlugin from "esbuild-plugin-vue3";
import * as fs from "fs";
import * as os from "os";
import * as path from "path";
import * as zlib from "zlib";
import * as crypto from "crypto";
import { execSync } from "child_process";
import wawoff2 from "wawoff2";
import { Resvg } from "@resvg/resvg-js";
import { generateShikiLanguageManifest } from "./generate-shiki-language-manifest.mjs";

// Esbuild plugin: rewrite any "monaco-editor*" import (including deep paths
// like monaco-editor/esm/vs/editor/editor.api) to the runtime URL
// /monaco-editor.js, marked external. Our custom bundle entry re-exports
// everything monaco-vim needs from that single file.
//
// We also bypass monaco-vim's package.json exports map: it routes the
// "browser" condition to a UMD bundle that esbuild wraps with a CJS
// require() shim, which then tries to require('/monaco-editor.js') at
// runtime and fails. Resolve directly to the ESM index.mjs instead.
function monacoExternalPlugin() {
  return {
    name: "monaco-external",
    setup(build) {
      build.onResolve({ filter: /^monaco-editor(\/|$)/ }, () => ({
        path: "/monaco-editor.js",
        external: true,
      }));
      const monacoVimEsm = path.resolve(process.cwd(), "node_modules/monaco-vim/dist/index.mjs");
      build.onResolve({ filter: /^monaco-vim$/ }, () => ({ path: monacoVimEsm }));
    },
  };
}

// Symbols Nerd Font Mono (MIT, https://github.com/ryanoasis/nerd-fonts), the
// terminal's fallback for icon glyphs; see the @font-face in src/styles.css.
// Rather than check in a binary, fetch the pinned TTF, convert it to woff2
// (~7s) and cache that in ~/.cache. wawoff2 is deterministic, so pin its
// output too: a corrupt cache entry is rebuilt and a changed encoder fails.
const NERD_FONT_URL =
  "https://raw.githubusercontent.com/ryanoasis/nerd-fonts/v3.4.0/patched-fonts/NerdFontsSymbolsOnly/SymbolsNerdFontMono-Regular.ttf";
const NERD_FONT_TTF_SHA256 = "f0f624d9b474bea1662cf7e862d44aebe1ae1f6c7f9cb7a0ca5d0e5ac9561c60";
const NERD_FONT_WOFF2_SHA256 = "9bccf9aeafc5b854a828abce4ecdd75f95bbc6c030e0182586900360ac02b49d";

const sha256 = (data) => crypto.createHash("sha256").update(data).digest("hex");

function verify(data, want, what) {
  const got = sha256(data);
  if (got !== want) throw new Error(`${what}: SHA-256 ${got}, want ${want}`);
  return data;
}

// Returns the verified woff2, from the cache or built into it.
async function symbolsNerdFont() {
  const cacheDir = process.env.XDG_CACHE_HOME || path.join(os.homedir(), ".cache");
  const cached = path.join(cacheDir, "shelley", "fonts", "SymbolsNerdFontMono.woff2");
  const hit = fs.existsSync(cached) && fs.readFileSync(cached);
  if (hit && sha256(hit) === NERD_FONT_WOFF2_SHA256) return hit;
  const res = await fetch(NERD_FONT_URL, { signal: AbortSignal.timeout(120_000) });
  if (!res.ok) throw new Error(`${NERD_FONT_URL}: HTTP ${res.status}`);
  const ttf = verify(Buffer.from(await res.arrayBuffer()), NERD_FONT_TTF_SHA256, NERD_FONT_URL);
  const woff2 = verify(Buffer.from(await wawoff2.compress(ttf)), NERD_FONT_WOFF2_SHA256, "wawoff2");
  fs.mkdirSync(path.dirname(cached), { recursive: true });
  fs.writeFileSync(cached, woff2);
  return woff2;
}

const isWatch = process.argv.includes("--watch");
const isProd = !isWatch;
const verbose = process.env.VERBOSE === "1" || process.env.VERBOSE === "true";
// Release builds (NO_SOURCEMAPS=1, set by release.yml) ship no JS source maps
// to keep the embedded binary small. Other builds emit them (gzip-compressed)
// so devtools work in development.
const dropSourceMaps = process.env.NO_SOURCEMAPS === "1";
// FAST_BUILD=1 is for builds whose output is only ever executed by tests, never
// shipped or debugged: CI's test steps. Source maps go away (nothing reads them
// headlessly) and the remaining assets compress at level 1 instead of 9. The
// output is byte-for-byte equivalent after decompression, so the server, the
// embed check and every test behave identically — only the artifact is bigger,
// which nothing downstream of a test run cares about. Worth ~5s per build, and
// CI builds the UI once per shelley step (8 of them).
const fastBuild = process.env.FAST_BUILD === "1";
const gzipLevel = fastBuild ? 1 : 9;
const noSourceMaps = dropSourceMaps || fastBuild;

function log(...args) {
  if (verbose) console.log(...args);
}

async function build() {
  const startTime = Date.now();
  try {
    // Keep fence-label recognition and worker grammar loaders aligned with the
    // exact bundled Shiki catalog installed by pnpm.
    await generateShikiLanguageManifest();

    // Ensure dist directory exists
    if (!fs.existsSync("dist")) {
      fs.mkdirSync("dist");
    }

    // Build Monaco editor worker separately (IIFE format for web worker)
    log("Building Monaco editor worker...");
    await esbuild.build({
      entryPoints: ["node_modules/monaco-editor/esm/vs/editor/editor.worker.js"],
      bundle: true,
      outfile: "dist/editor.worker.js",
      format: "iife",
      minify: isProd,
      sourcemap: !noSourceMaps,
    });

    // Build @pierre/diffs worker for syntax highlighting (IIFE format for web worker)
    log("Building diffs worker...");
    await esbuild.build({
      entryPoints: ["src/diffs-worker.ts"],
      bundle: true,
      outfile: "dist/diffs-worker.js",
      format: "iife",
      minify: isProd,
      sourcemap: !noSourceMaps,
    });

    // Build fenced-markdown Shiki syntax highlighting worker separately so
    // initialization and tokenization never run on the UI thread.
    log("Building markdown highlight worker...");
    await esbuild.build({
      entryPoints: ["src/markdown-highlight-worker.ts"],
      bundle: true,
      outfile: "dist/markdown-highlight-worker.js",
      format: "iife",
      minify: isProd,
      sourcemap: !noSourceMaps,
    });

    // Build Monaco editor as a separate chunk (JS + CSS).
    // We bundle through src/monaco-bundle-entry.js so we can also surface
    // the internal modules monaco-vim depends on (ShiftCommand) as named
    // exports of /monaco-editor.js — that way monaco-vim runs against the
    // *same* Monaco instance the rest of the app loads.
    log("Building Monaco editor bundle...");
    await esbuild.build({
      entryPoints: ["src/monaco-bundle-entry.js"],
      bundle: true,
      outfile: "dist/monaco-editor.js",
      format: "esm",
      minify: isProd,
      sourcemap: !noSourceMaps,
      loader: {
        ".ttf": "file",
      },
    });

    // ghostty-web, the default terminal emulator, with its WebAssembly inlined.
    // Fetched as /ghostty-web.js when the first terminal opens.
    log("Building ghostty-web bundle...");
    await esbuild.build({
      entryPoints: ["node_modules/ghostty-web/dist/ghostty-web.js"],
      bundle: true,
      outfile: "dist/ghostty-web.js",
      format: "esm",
      minify: isProd,
      sourcemap: !noSourceMaps,
    });

    // Build the Vue 3 + PrimeVue app (src/vue/main.ts). It emits dist/main.js +
    // dist/main.css, which index.html links statically (see src/index.html).
    log("Building main application (src/vue/main.ts)...");
    await esbuild.build({
      entryPoints: ["src/vue/main.ts"],
      bundle: true,
      outfile: "dist/main.js",
      format: "esm",
      minify: isProd,
      sourcemap: !noSourceMaps,
      metafile: true,
      external: ["monaco-editor", "/monaco-editor.js", "/ghostty-web.js"],
      loader: {
        ".png": "dataurl",
        ".svg": "text",
        ".woff": "dataurl",
        ".woff2": "dataurl",
        ".ttf": "dataurl",
        ".eot": "dataurl",
      },
      // Prefer ESM entry points so dynamic imports (e.g. monaco-vim) end
      // up using `import` rather than CJS `require` (which esbuild can't
      // emit at runtime in the browser).
      // monaco-vim's package.json exports a UMD bundle under the "browser"
      // condition; esbuild picks that by default and wraps it in a CJS
      // shim that requires() the external /monaco-editor.js at runtime,
      // which fails in the browser. Force resolution to its ESM build.

      // monaco-vim imports specific submodules of monaco-editor. Rewrite
      // those to the same runtime URL the rest of the app uses, so we end
      // up with a single Monaco instance instead of two. The rewritten
      // imports are marked external (above) so esbuild emits them as-is.
      plugins: [monacoExternalPlugin(), vuePlugin()],
    });

    await esbuild.build({
      entryPoints: ["src/vue/debug-timelines.js"],
      bundle: true,
      outfile: "dist/debug-timelines.js",
      format: "iife",
      minify: isProd,
      plugins: [vuePlugin()],
    });

    // /static/excalidraw/skill.js: self-contained Excalidraw + React +
    // skill helper bundle. The host React app fetches it same-origin and
    // streams it into the sandboxed `output_iframe` iframe via
    // postMessage; the iframe wraps it in a Blob and import()s it from
    // its own opaque origin, sidestepping CORS.
    log("Building /static/excalidraw bundle...");
    fs.mkdirSync("dist/static/excalidraw", { recursive: true });
    await esbuild.build({
      entryPoints: ["src/excalidraw-skill.js"],
      bundle: true,
      outfile: "dist/static/excalidraw/skill.js",
      format: "esm",
      minify: isProd,
      sourcemap: false,
      define: { "process.env.NODE_ENV": '"production"' },
      // Inline the stylesheet and any referenced font/icon assets as data
      // URLs so the resulting module is fully self-contained.
      loader: {
        ".css": "text",
        ".woff": "dataurl",
        ".woff2": "dataurl",
        ".ttf": "dataurl",
        ".png": "dataurl",
        ".svg": "dataurl",
      },
    });

    // Copy static files
    fs.copyFileSync("src/index.html", "dist/index.html");
    const btwStylesImport = '@import "./btw.css";';
    const styles = fs.readFileSync("src/styles.css", "utf8");
    if (styles.split(btwStylesImport).length !== 2) {
      throw new Error("styles.css must import btw.css exactly once");
    }
    fs.writeFileSync(
      "dist/styles.css",
      styles.replace(btwStylesImport, fs.readFileSync("src/btw.css", "utf8").trimEnd()),
    );

    // Copy assets (icons, manifest, etc.)
    const assetsDir = "src/assets";
    if (fs.existsSync(assetsDir)) {
      for (const file of fs.readdirSync(assetsDir)) {
        fs.copyFileSync(`${assetsDir}/${file}`, `dist/${file}`);
      }
    }
    fs.writeFileSync("dist/SymbolsNerdFontMono.woff2", await symbolsNerdFont());
    const appIcon = fs.readFileSync("src/app-icon.svg");
    for (const [file, size] of [
      ["icon-192.png", 192],
      ["icon-512.png", 512],
      ["apple-touch-icon.png", 180],
    ]) {
      const png = new Resvg(appIcon, {
        fitTo: { mode: "width", value: size },
        font: { loadSystemFonts: false }, // no text to draw
      })
        .render()
        .asPng();
      fs.writeFileSync(`dist/${file}`, png);
    }

    // Write build info
    // Get the absolute path to the src directory for staleness checking
    const srcDir = new URL("../src", import.meta.url).pathname;

    // Get git commit info
    let commit = "";
    let commitTime = "";
    let modified = false;
    try {
      commit = execSync("git rev-parse HEAD", { encoding: "utf8" }).trim();
      commitTime = execSync("git log -1 --format=%cI", { encoding: "utf8" }).trim();
      // Check for modifications, excluding the dist/ directory (which we're currently building)
      const status = execSync("git status --porcelain --ignore-submodules", { encoding: "utf8" });
      // Filter out dist/ changes since those are expected during build
      const significantChanges = status
        .split("\n")
        .filter((line) => line.trim() && !line.includes("dist/"));
      modified = significantChanges.length > 0;
    } catch (e) {
      // Git not available or not a git repo
    }

    const buildInfo = {
      timestamp: Date.now(),
      date: new Date().toISOString(),
      srcDir: srcDir,
      commit: commit,
      commitTime: commitTime,
      modified: modified,
    };
    fs.writeFileSync("dist/build-info.json", JSON.stringify(buildInfo, null, 2));

    // Generate gzip versions of large files and remove originals to reduce binary size
    // The server will decompress on-the-fly for the rare clients that don't support gzip
    log("\nGenerating gzip compressed files...");
    const filesToCompress = [
      "monaco-editor.js",
      "ghostty-web.js",
      "editor.worker.js",
      "diffs-worker.js",
      "markdown-highlight-worker.js",
      "monaco-editor.css",
      "styles.css",
      "main.js",
      "main.css",
      "debug-timelines.js",
      "static/excalidraw/skill.js",
      "SymbolsNerdFontMono.woff2",
    ];
    const checksums = {};
    let totalOrigSize = 0;
    let totalGzSize = 0;

    for (const file of filesToCompress) {
      const inputPath = `dist/${file}`;
      const outputPath = `dist/${file}.gz`;
      if (fs.existsSync(inputPath)) {
        const input = fs.readFileSync(inputPath);
        const compressed = zlib.gzipSync(input, { level: gzipLevel });
        fs.writeFileSync(outputPath, compressed);

        // Compute SHA256 of the compressed content for ETag
        const hash = crypto.createHash("sha256").update(compressed).digest("hex").slice(0, 16);
        checksums[file] = hash;

        totalOrigSize += input.length;
        totalGzSize += compressed.length;

        if (verbose) {
          const origKb = (input.length / 1024).toFixed(1);
          const gzKb = (compressed.length / 1024).toFixed(1);
          const ratio = ((compressed.length / input.length) * 100).toFixed(0);
          console.log(`  ${file}: ${origKb} KB -> ${gzKb} KB gzip (${ratio}%) [${hash}]`);
        }

        // Remove original to save space in embedded binary
        fs.unlinkSync(inputPath);
      }
    }

    // Source maps are large (tens of MB uncompressed) and only fetched by
    // browsers with devtools open. Release builds (NO_SOURCEMAPS=1, set by
    // release.yml) drop them entirely; other builds gzip them so the embedded
    // binary stays small while devtools still work. The server serves
    // <name>.map from the embedded <name>.map.gz, exactly as for .js/.css.
    log(noSourceMaps ? "\nRemoving source maps..." : "\nGzipping source maps...");
    for (const file of fs.readdirSync("dist")) {
      if (noSourceMaps) {
        // dist/ isn't cleaned between builds, so also drop .map.gz left over
        // from a previous dev build.
        if (file.endsWith(".map") || file.endsWith(".map.gz")) {
          fs.unlinkSync(`dist/${file}`);
        }
        continue;
      }
      if (!file.endsWith(".map")) continue;
      const inputPath = `dist/${file}`;
      const input = fs.readFileSync(inputPath);
      const compressed = zlib.gzipSync(input, { level: gzipLevel });
      fs.writeFileSync(`${inputPath}.gz`, compressed);
      // Record a content checksum so the server can emit ETags and answer 304s
      // for source maps, matching the other compressed assets.
      checksums[file] = crypto.createHash("sha256").update(compressed).digest("hex").slice(0, 16);
      fs.unlinkSync(inputPath);
      if (verbose) {
        const origKb = (input.length / 1024).toFixed(1);
        const gzKb = (compressed.length / 1024).toFixed(1);
        console.log(`  ${file}: ${origKb} KB -> ${gzKb} KB gzip`);
      }
    }

    // Write checksums for ETag support
    fs.writeFileSync("dist/checksums.json", JSON.stringify(checksums, null, 2));
    log("\nChecksums written to dist/checksums.json");

    if (verbose) {
      console.log("\nOther files:");
      const otherFiles = fs
        .readdirSync("dist")
        .filter((f) => (f.endsWith(".ttf") || f.endsWith(".map")) && !f.endsWith(".gz"));
      for (const file of otherFiles.sort()) {
        const stats = fs.statSync(`dist/${file}`);
        const sizeKb = (stats.size / 1024).toFixed(1);
        console.log(`  ${file}: ${sizeKb} KB`);
      }
    }

    const elapsed = ((Date.now() - startTime) / 1000).toFixed(1);
    const totalGzKb = (totalGzSize / 1024).toFixed(0);
    console.log(`UI built in ${elapsed}s (${totalGzKb} KB gzipped)`);
  } catch (error) {
    console.error("Build failed:", error);
    process.exit(1);
  }
}

build();
