import * as esbuild from 'esbuild';
import * as fs from 'fs';
import * as path from 'path';
import * as zlib from 'zlib';
import * as crypto from 'crypto';
import { execSync } from 'child_process';

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
    name: 'monaco-external',
    setup(build) {
      build.onResolve({ filter: /^monaco-editor(\/|$)/ }, () => ({
        path: '/monaco-editor.js',
        external: true,
      }));
      const monacoVimEsm = path.resolve(
        process.cwd(),
        'node_modules/monaco-vim/dist/index.mjs',
      );
      build.onResolve({ filter: /^monaco-vim$/ }, () => ({ path: monacoVimEsm }));
    },
  };
}

const isWatch = process.argv.includes('--watch');
const isProd = !isWatch;
const verbose = process.env.VERBOSE === '1' || process.env.VERBOSE === 'true';
// Release builds (NO_SOURCEMAPS=1, set by release.yml) ship no JS source maps
// to keep the embedded binary small. Other builds emit them (gzip-compressed)
// so devtools work in development.
const dropSourceMaps = process.env.NO_SOURCEMAPS === '1';

function log(...args) {
  if (verbose) console.log(...args);
}

async function build() {
  const startTime = Date.now();
  try {
    // Ensure dist directory exists
    if (!fs.existsSync('dist')) {
      fs.mkdirSync('dist');
    }

    // Build Monaco editor worker separately (IIFE format for web worker)
    log('Building Monaco editor worker...');
    await esbuild.build({
      entryPoints: ['node_modules/monaco-editor/esm/vs/editor/editor.worker.js'],
      bundle: true,
      outfile: 'dist/editor.worker.js',
      format: 'iife',
      minify: isProd,
      sourcemap: !dropSourceMaps,
    });

    // Build @pierre/diffs worker for syntax highlighting (IIFE format for web worker)
    log('Building diffs worker...');
    await esbuild.build({
      entryPoints: ['src/diffs-worker.ts'],
      bundle: true,
      outfile: 'dist/diffs-worker.js',
      format: 'iife',
      minify: isProd,
      sourcemap: !dropSourceMaps,
      alias: { '@': path.resolve(process.cwd(), 'src') },
    });

    // Build Monaco editor as a separate chunk (JS + CSS).
    // We bundle through src/monaco-bundle-entry.js so we can also surface
    // the internal modules monaco-vim depends on (ShiftCommand) as named
    // exports of /monaco-editor.js — that way monaco-vim runs against the
    // *same* Monaco instance the rest of the app loads.
    log('Building Monaco editor bundle...');
    await esbuild.build({
      entryPoints: ['src/monaco-bundle-entry.js'],
      bundle: true,
      outfile: 'dist/monaco-editor.js',
      format: 'esm',
      minify: isProd,
      sourcemap: !dropSourceMaps,
      loader: {
        '.ttf': 'file',
      },
    });

    // Build main app - exclude monaco-editor, we'll load it dynamically
    log('Building main application...');
    const result = await esbuild.build({
      entryPoints: ['src/main.tsx'],
      bundle: true,
      outfile: 'dist/main.js',
      format: 'esm',
      minify: isProd,
      sourcemap: !dropSourceMaps,
      metafile: true,
      alias: { '@': path.resolve(process.cwd(), 'src') },
      external: ['monaco-editor', '/monaco-editor.js'],
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
      plugins: [monacoExternalPlugin()],
    });

    // /static/excalidraw/skill.js: self-contained Excalidraw + React +
    // skill helper bundle. The host React app fetches it same-origin and
    // streams it into the sandboxed `output_iframe` iframe via
    // postMessage; the iframe wraps it in a Blob and import()s it from
    // its own opaque origin, sidestepping CORS.
    log('Building /static/excalidraw bundle...');
    fs.mkdirSync('dist/static/excalidraw', { recursive: true });
    await esbuild.build({
      entryPoints: ['src/excalidraw-skill.js'],
      bundle: true,
      outfile: 'dist/static/excalidraw/skill.js',
      format: 'esm',
      minify: isProd,
      sourcemap: false,
      define: { 'process.env.NODE_ENV': '"production"' },
      // Inline the stylesheet and any referenced font/icon assets as data
      // URLs so the resulting module is fully self-contained.
      loader: {
        '.css': 'text',
        '.woff': 'dataurl', '.woff2': 'dataurl', '.ttf': 'dataurl',
        '.png': 'dataurl', '.svg': 'dataurl',
      },
    });

    // Compile Tailwind v4 (shadcn theme) via the standalone CLI into
    // dist/tailwind.css. Run as its own step (not through esbuild) so it stays
    // off the main.css graph that already absorbs xterm/excalidraw CSS. The CLI
    // resolves the @import "tailwindcss" / "shadcn/tailwind.css" / "@fontsource-*"
    // specifiers from node_modules.
    log('Compiling Tailwind CSS...');
    execSync(
      `node_modules/.bin/tailwindcss -i src/tailwind.css -o dist/tailwind.css${isProd ? ' --minify' : ''}`,
      { stdio: verbose ? 'inherit' : 'pipe' },
    );

    // The compiled CSS references the IBM Plex Sans variable font as
    // url(./files/<subset>-wght-normal.woff2) — relative to /tailwind.css, i.e.
    // /files/... at runtime. Copy those woff2 files into dist/files/ so the
    // self-contained binary serves them. woff2 is already compressed (not gzipped).
    const fontSrcDir = 'node_modules/@fontsource-variable/ibm-plex-sans/files';
    const fontDstDir = 'dist/files';
    fs.mkdirSync(fontDstDir, { recursive: true });
    for (const file of fs.readdirSync(fontSrcDir)) {
      if (file.endsWith('-wght-normal.woff2')) {
        fs.copyFileSync(`${fontSrcDir}/${file}`, `${fontDstDir}/${file}`);
      }
    }

    // Copy static files
    fs.copyFileSync('src/index.html', 'dist/index.html');

    // Copy assets (icons, manifest, etc.)
    const assetsDir = 'src/assets';
    if (fs.existsSync(assetsDir)) {
      for (const file of fs.readdirSync(assetsDir)) {
        fs.copyFileSync(`${assetsDir}/${file}`, `dist/${file}`);
      }
    }

    // Write build info
    // Get the absolute path to the src directory for staleness checking
    const srcDir = new URL('../src', import.meta.url).pathname;

    // Get git commit info
    let commit = '';
    let commitTime = '';
    let modified = false;
    try {
      commit = execSync('git rev-parse HEAD', { encoding: 'utf8' }).trim();
      commitTime = execSync('git log -1 --format=%cI', { encoding: 'utf8' }).trim();
      // Check for modifications, excluding the dist/ directory (which we're currently building)
      const status = execSync('git status --porcelain --ignore-submodules', { encoding: 'utf8' });
      // Filter out dist/ changes since those are expected during build
      const significantChanges = status.split('\n').filter(line =>
        line.trim() && !line.includes('dist/')
      );
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
    fs.writeFileSync('dist/build-info.json', JSON.stringify(buildInfo, null, 2));

    // Generate gzip versions of large files and remove originals to reduce binary size
    // The server will decompress on-the-fly for the rare clients that don't support gzip
    log('\nGenerating gzip compressed files...');
    const filesToCompress = [
      'monaco-editor.js', 'editor.worker.js', 'diffs-worker.js', 'main.js',
      'monaco-editor.css', 'main.css', 'tailwind.css',
      'static/excalidraw/skill.js',
    ];
    const checksums = {};
    let totalOrigSize = 0;
    let totalGzSize = 0;

    for (const file of filesToCompress) {
      const inputPath = `dist/${file}`;
      const outputPath = `dist/${file}.gz`;
      if (fs.existsSync(inputPath)) {
        const input = fs.readFileSync(inputPath);
        const compressed = zlib.gzipSync(input, { level: 9 });
        fs.writeFileSync(outputPath, compressed);

        // Compute SHA256 of the compressed content for ETag
        const hash = crypto.createHash('sha256').update(compressed).digest('hex').slice(0, 16);
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
    log(dropSourceMaps ? '\nRemoving source maps...' : '\nGzipping source maps...');
    for (const file of fs.readdirSync('dist')) {
      if (dropSourceMaps) {
        // dist/ isn't cleaned between builds, so also drop .map.gz left over
        // from a previous dev build.
        if (file.endsWith('.map') || file.endsWith('.map.gz')) {
          fs.unlinkSync(`dist/${file}`);
        }
        continue;
      }
      if (!file.endsWith('.map')) continue;
      const inputPath = `dist/${file}`;
      const input = fs.readFileSync(inputPath);
      const compressed = zlib.gzipSync(input, { level: 9 });
      fs.writeFileSync(`${inputPath}.gz`, compressed);
      // Record a content checksum so the server can emit ETags and answer 304s
      // for source maps, matching the other compressed assets.
      checksums[file] = crypto.createHash('sha256').update(compressed).digest('hex').slice(0, 16);
      fs.unlinkSync(inputPath);
      if (verbose) {
        const origKb = (input.length / 1024).toFixed(1);
        const gzKb = (compressed.length / 1024).toFixed(1);
        console.log(`  ${file}: ${origKb} KB -> ${gzKb} KB gzip`);
      }
    }

    // Write checksums for ETag support
    fs.writeFileSync('dist/checksums.json', JSON.stringify(checksums, null, 2));
    log('\nChecksums written to dist/checksums.json');

    if (verbose) {
      console.log('\nOther files:');
      const otherFiles = fs.readdirSync('dist').filter(f =>
        (f.endsWith('.ttf') || f.endsWith('.map')) && !f.endsWith('.gz')
      );
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
    console.error('Build failed:', error);
    process.exit(1);
  }
}

build();
