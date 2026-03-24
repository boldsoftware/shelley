import * as esbuild from 'esbuild';
import * as fs from 'fs';
import * as zlib from 'zlib';
import * as crypto from 'crypto';
import { execSync } from 'child_process';

const isWatch = process.argv.includes('--watch');
const isProd = !isWatch;
const verbose = process.env.VERBOSE === '1' || process.env.VERBOSE === 'true';

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
      sourcemap: true,
    });

    // Build @pierre/diffs worker for syntax highlighting (IIFE format for web worker)
    log('Building diffs worker...');
    await esbuild.build({
      entryPoints: ['src/diffs-worker.ts'],
      bundle: true,
      outfile: 'dist/diffs-worker.js',
      format: 'iife',
      minify: isProd,
      sourcemap: true,
    });

    // Build Monaco editor as a separate chunk (JS + CSS)
    log('Building Monaco editor bundle...');
    await esbuild.build({
      entryPoints: ['node_modules/monaco-editor/esm/vs/editor/editor.main.js'],
      bundle: true,
      outfile: 'dist/monaco-editor.js',
      format: 'esm',
      minify: isProd,
      sourcemap: true,
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
      sourcemap: true,
      metafile: true,
      external: ['monaco-editor', '/monaco-editor.js'],
    });

    // Copy static files
    fs.copyFileSync('src/index.html', 'dist/index.html');
    fs.copyFileSync('src/styles.css', 'dist/styles.css');

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
    const filesToCompress = ['monaco-editor.js', 'editor.worker.js', 'diffs-worker.js', 'main.js', 'monaco-editor.css', 'styles.css', 'main.css'];
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
