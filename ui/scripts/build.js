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

// Esbuild plugin for the main build: the bundles it loads on demand are
// addressed by their /assets/ URLs (see buildAsset), which stay external, and
// any "monaco-editor*" import (including deep paths like
// monaco-editor/esm/vs/editor/editor.api) becomes the Monaco bundle's URL, so
// the app shares the one Monaco instance that bundle loads.
function assetsExternalPlugin(monacoUrl) {
  return {
    name: "assets-external",
    setup(build) {
      build.onResolve({ filter: /^\/assets\// }, (args) => ({ path: args.path, external: true }));
      build.onResolve({ filter: /^monaco-editor(\/|$)/ }, () => ({
        path: monacoUrl,
        external: true,
      }));
    },
  };
}

// Esbuild plugin: an SVG that CSS references with url() (primeicons' legacy
// SVG font) is a file of its own, though scripts import SVGs as text.
function cssSvgFilePlugin() {
  return {
    name: "css-svg-file",
    setup(build) {
      build.onResolve({ filter: /\.svg([?#].*)?$/ }, async (args) => {
        if (args.kind !== "url-token" || args.pluginData?.cssSvg) return undefined;
        const resolved = await build.resolve(args.path, {
          kind: args.kind,
          importer: args.importer,
          resolveDir: args.resolveDir,
          pluginData: { cssSvg: true },
        });
        return resolved.errors.length ? resolved : { ...resolved, namespace: "css-svg-file" };
      });
      build.onLoad({ filter: /.*/, namespace: "css-svg-file" }, (args) => ({
        contents: fs.readFileSync(args.path),
        loader: "file",
      }));
    },
  };
}

// Esbuild plugin: bypass monaco-vim's package.json exports map, which routes
// the "browser" condition to a UMD bundle that esbuild wraps with a CJS
// require() shim. Resolve directly to the ESM index.mjs instead, so its
// imports of monaco-editor bind to the Monaco bundled beside it.
function monacoVimEsmPlugin() {
  return {
    name: "monaco-vim-esm",
    setup(build) {
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

// Bundles one minified script and returns its text.
async function esbuildText(options) {
  const result = await esbuild.build({ ...options, bundle: true, minify: true, write: false });
  return result.outputFiles[0].text.trim();
}

// Bundles entry into dist/assets/ under names that carry a hash of the
// content, and returns the /assets/ URLs of the entry's script (js) and of its
// stylesheet (css) if it has one.
async function buildAsset(name, entry, options) {
  const result = await esbuild.build({
    entryPoints: { [name]: entry },
    bundle: true,
    outdir: "dist/assets",
    entryNames: "[name]-[hash]",
    chunkNames: "[name]-[hash]",
    assetNames: "[name]-[hash]",
    minify: isProd,
    sourcemap: !noSourceMaps,
    metafile: true,
    ...options,
  });
  const url = (output) => "/" + path.relative("dist", output);
  // With splitting, what entry imports with import() are entry points too.
  const entryPoint = path.relative(".", fs.realpathSync(entry));
  const [js, meta] = Object.entries(result.metafile.outputs).find(
    ([, o]) => o.entryPoint === entryPoint,
  );
  return { js: url(js), css: meta.cssBundle && url(meta.cssBundle) };
}

async function build() {
  const startTime = Date.now();
  try {
    // Keep fence-label recognition and worker grammar loaders aligned with the
    // exact bundled Shiki catalog installed by pnpm.
    await generateShikiLanguageManifest();

    // Start empty: go:embed takes whatever is in dist/, and CI restores dist/
    // as hardlinks into a shared cache, which writing in place would change.
    fs.rmSync("dist", { recursive: true, force: true });
    fs.mkdirSync("dist");

    // Bundles the app loads on demand, and the fonts its CSS uses, are named
    // after their content in dist/assets/, which the server lets browsers keep
    // for good (server/handlers.go).

    // Monaco's editor worker, a classic worker (IIFE), which Monaco starts from
    // the URL MonacoEnvironment.getWorkerUrl gives it.
    log("Building Monaco editor worker...");
    const editorWorker = await buildAsset(
      "editor.worker",
      "node_modules/monaco-editor/esm/vs/editor/editor.worker.js",
      {
        format: "iife",
      },
    );

    // The @pierre/diffs syntax highlighting worker, a classic worker (IIFE).
    log("Building diffs worker...");
    const diffsWorker = await buildAsset("diffs-worker", "src/diffs-worker.ts", { format: "iife" });

    // Build fenced-markdown Shiki syntax highlighting worker separately so
    // initialization and tokenization never run on the UI thread. It is a module
    // worker, split so that each grammar it imports is a file of its own,
    // fetched when a fence first asks for it.
    log("Building markdown highlight worker...");
    const markdownHighlightWorker = await buildAsset(
      "markdown-highlight-worker",
      "src/markdown-highlight-worker.ts",
      { format: "esm", splitting: true },
    );

    // Build Monaco editor as a separate chunk (JS + CSS).
    // We bundle through src/monaco-bundle-entry.js so that monaco-vim ships
    // in the Monaco bundle too and runs against the *same* Monaco instance
    // the rest of the app loads.
    log("Building Monaco editor bundle...");
    const monaco = await buildAsset("monaco-editor", "src/monaco-bundle-entry.js", {
      format: "esm",
      loader: {
        ".ttf": "file",
      },
      plugins: [monacoVimEsmPlugin()],
    });

    // ghostty-web, the default terminal emulator, with its WebAssembly inlined.
    // Fetched when the first terminal opens.
    log("Building ghostty-web bundle...");
    const ghostty = await buildAsset(
      "ghostty-web",
      "node_modules/ghostty-web/dist/ghostty-web.js",
      {
        format: "esm",
      },
    );

    // The URLs the main build compiles in for the bundles above.
    const assetUrls = {
      __EDITOR_WORKER_URL__: editorWorker.js,
      __DIFFS_WORKER_URL__: diffsWorker.js,
      __MARKDOWN_HIGHLIGHT_WORKER_URL__: markdownHighlightWorker.js,
      __MONACO_URL__: monaco.js,
      __MONACO_CSS_URL__: monaco.css,
      __GHOSTTY_URL__: ghostty.js,
    };
    for (const [name, url] of Object.entries(assetUrls)) {
      if (!url) throw new Error(`${name}: no output`);
    }

    // The bookmarklet's page script, embedded in the app's bookmarklet link and
    // in dist/shelley-bookmarklet.html, a standalone page for making one.
    log("Building bookmarklet...");
    const bookmarklet = await esbuildText({
      entryPoints: ["src/bookmarklet/page.ts"],
      format: "iife",
      target: "es2020",
    });
    const bookmarkletDefine = { __BOOKMARKLET_SOURCE__: JSON.stringify(bookmarklet) };
    const standalone = await esbuildText({
      entryPoints: ["src/bookmarklet/standalone.ts"],
      format: "iife",
      define: bookmarkletDefine,
    });
    const standaloneCss = fs.readFileSync("src/bookmarklet/standalone.css", "utf8");
    if (/<\/script|<!--/i.test(standalone) || /<\/style/i.test(standaloneCss)) {
      throw new Error("bookmarklet: standalone.ts or .css can't be inlined");
    }
    const standaloneHtml = fs
      .readFileSync("src/bookmarklet/standalone.html", "utf8")
      .split('<link rel="stylesheet" href="standalone.css" />')
      .join(`<style>${standaloneCss}</style>`)
      .split('<script src="standalone.js"></script>')
      .join(`<script>${standalone}</script>`);
    if (/standalone\.(css|js)/.test(standaloneHtml)) {
      throw new Error("standalone.html: unexpected standalone.css or standalone.js reference");
    }
    fs.writeFileSync("dist/shelley-bookmarklet.html", standaloneHtml);

    // The view side of the MCP Apps bridge, injected into output_html pages.
    log("Building app bridge...");
    const appBridge = await esbuildText({
      entryPoints: ["src/appBridge/view.ts"],
      format: "iife",
      target: "es2020",
    });
    if (/<\/script|<!--/i.test(appBridge)) throw new Error("appBridge: view.ts can't be inlined");

    // Build the Vue 3 + PrimeVue app (src/vue/main.ts). It emits dist/main.js +
    // dist/main.css, which index.html links statically (see src/index.html).
    // What main.js imports with import() (Shiki's grammars and themes, say),
    // and the fonts main.css uses, go to dist/assets/ under content hashes.
    log("Building main application (src/vue/main.ts)...");
    const mainBuild = await esbuild.build({
      entryPoints: { main: "src/vue/main.ts" },
      bundle: true,
      outdir: "dist",
      entryNames: "[name]",
      chunkNames: "assets/[name]-[hash]",
      assetNames: "assets/[name]-[hash]",
      splitting: true,
      format: "esm",
      minify: isProd,
      sourcemap: !noSourceMaps,
      metafile: true,
      define: {
        ...bookmarkletDefine,
        __APP_BRIDGE_SOURCE__: JSON.stringify(appBridge),
        ...Object.fromEntries(Object.entries(assetUrls).map(([k, v]) => [k, JSON.stringify(v)])),
      },
      loader: {
        ".png": "dataurl",
        ".svg": "text",
        ".woff": "file",
        ".woff2": "file",
        ".ttf": "file",
        ".eot": "file",
      },
      plugins: [assetsExternalPlugin(monaco.js), cssSvgFilePlugin(), vuePlugin()],
    });
    // Monaco is ~3.7MB of script: the app loads it with import(), once the
    // page is idle or an editor opens. A static import of "monaco-editor"
    // anywhere main.js reaches without import() would make every page load and
    // run all of Monaco before it could start.
    const staticImports = (file, seen = new Set()) => {
      if (seen.has(file)) return seen;
      seen.add(file);
      for (const i of mainBuild.metafile.outputs[file]?.imports ?? []) {
        if (i.kind === "import-statement") staticImports(i.path, seen);
      }
      return seen;
    };
    if (staticImports("dist/main.js").has(monaco.js)) {
      throw new Error(
        "dist/main.js statically imports Monaco: load it with import() (services/monaco.ts)",
      );
    }

    await esbuild.build({
      entryPoints: ["src/vue/debug-timelines.js"],
      bundle: true,
      outfile: "dist/debug-timelines.js",
      format: "iife",
      minify: isProd,
      plugins: [vuePlugin()],
    });

    await esbuild.build({
      entryPoints: ["src/vue/debug-context.ts"],
      bundle: true,
      outfile: "dist/debug-context.js",
      format: "iife",
      minify: isProd,
      plugins: [vuePlugin()],
    });

    // /static/excalidraw/skill.js: self-contained Excalidraw + React +
    // skill helper bundle. The host React app fetches it same-origin and
    // streams it into the sandboxed `output_html` iframe via
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
    // Workspace mode's favicon (the server inlines it) and home-screen icon.
    fs.copyFileSync("src/vue/assets/exe-fish.svg", "dist/exe-fish.svg");
    for (const [icon, file, size] of [
      ["src/app-icon.svg", "icon-192.png", 192],
      ["src/app-icon.svg", "icon-512.png", 512],
      ["src/app-icon.svg", "apple-touch-icon.png", 180],
      ["src/exe-app-icon.svg", "exe-apple-touch-icon.png", 180],
    ]) {
      const png = new Resvg(fs.readFileSync(icon), {
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
    // dist/assets/ is stored gzipped whole (source maps below).
    const assets = fs.readdirSync("dist/assets").map((f) => `assets/${f}`);
    // go:embed skips names that start with "." or "_".
    const unembeddable = assets.filter((f) => /^assets\/[._]/.test(f));
    if (unembeddable.length) throw new Error(`go:embed would skip ${unembeddable.join(", ")}`);
    const filesToCompress = [
      ...assets.filter((f) => !f.endsWith(".map")),
      "styles.css",
      "main.js",
      "main.css",
      "debug-timelines.js",
      "debug-context.js",
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
    log("\nGzipping source maps...");
    for (const file of [...fs.readdirSync("dist"), ...assets]) {
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

    const elapsed = ((Date.now() - startTime) / 1000).toFixed(1);
    const totalGzKb = (totalGzSize / 1024).toFixed(0);
    console.log(`UI built in ${elapsed}s (${totalGzKb} KB gzipped)`);
  } catch (error) {
    console.error("Build failed:", error);
    process.exit(1);
  }
}

build();
