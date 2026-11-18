// Playwright global setup: starts one shelley test server per worker, each on
// a random port (reported via --port-file) with its own DB, HOME and cwd.
// Workers pick theirs up in playwright.config.ts (scripts/worker-server.ts).
// Server output goes to <outputDir>/servers/<n>.log rather than the run's
// output, where dozens of servers' request logs would bury the results.

import { execFileSync, execSync, spawn, type ChildProcess } from 'child_process';
import {
  closeSync, existsSync, mkdirSync, mkdtempSync, openSync, readFileSync, rmSync, writeFileSync,
} from 'fs';
import { createServer, type Server } from 'http';
import type { AddressInfo } from 'net';
import { tmpdir } from 'os';
import path from 'path';
import { fileURLToPath } from 'url';
import type { FullConfig } from '@playwright/test';
import { hermeticGitEnvironment, installHermeticGitEnvironment } from './git-test-env';
import { WORKER_SERVERS_ENV, useWorkerServer, type WorkerServer } from './worker-server';

const __filename = fileURLToPath(import.meta.url);
const __dirname = path.dirname(__filename);
const shelleyDir = path.resolve(__dirname, '../..');
const binPath = path.join(shelleyDir, 'bin', 'shelley');

interface ServerProcess {
  process: ChildProcess;
  // Names the server in errors: "server 3 (test-results/servers/3.log)".
  name: string;
}

// One entry per Playwright worker (see startTestEnvironment).
let serverProcesses: ServerProcess[] = [];
let tempDirs: string[] = [];
let liveProvider: Server | null = null;

// GPT-Live's session endpoint, as far as Shelley sees it: each call gets a
// new session id. Media and events never reach it; the specs fake the
// browser's WebRTC side.
async function startLiveProvider(): Promise<string> {
  let sessions = 0;
  liveProvider = createServer((req, res) => {
    req.resume();
    res.writeHead(201, { 'Content-Type': 'application/json' });
    res.end(JSON.stringify({ session: { id: `live_${++sessions}` }, transport: { type: 'webrtc', sdp: 'v=0' } }));
  });
  await new Promise<void>((resolve) => liveProvider!.listen(0, '127.0.0.1', resolve));
  return `http://127.0.0.1:${(liveProvider.address() as AddressInfo).port}/v1/live/sessions`;
}

// Terminal sessions outlive the server by design, so end the ones the tests
// left behind, along with every process group in each owned PTY session.
// Interactive shells put foreground and background jobs in separate groups;
// killing only the forkpty child's group leaves those jobs running forever.
// One process-table scan covers every server's directory.
function killTerminalSessions(dirs: string[]) {
  if (dirs.length === 0) return;
  const prefixes = dirs.map((dir) => `exe-scroll: session ${dir}/`);
  // Shared CI hosts can have process tables larger than Node's 1 MiB default.
  const procs = execFileSync('ps', ['-axo', 'pid=,ppid=,pgid=,sess=,command='], {
    encoding: 'utf8',
    maxBuffer: 64 * 1024 * 1024,
  })
    .split('\n')
    .flatMap((line) => {
      const m = /^\s*(\d+)\s+(\d+)\s+(\d+)\s+(\S+)\s+(.*)$/.exec(line);
      return m ? [{
        pid: Number(m[1]), ppid: Number(m[2]), pgid: Number(m[3]),
        session: m[4], command: m[5],
      }] : [];
    });
  const servers = procs.filter((p) => prefixes.some((prefix) => p.command.startsWith(prefix)));
  const serverSessions = new Map(servers.map((p) => [p.pid, p.session]));
  // Linux ps prints the numeric SID; macOS ps prints a hex session pointer.
  // forkpty's direct child leads its own group in a *different* session
  // from the server. Match that session token exactly for every group.
  const sessions = new Set(procs.filter((p) => p.pid === p.pgid && serverSessions.has(p.ppid)
    && p.session !== serverSessions.get(p.ppid)).map((p) => p.session));
  const kill = (pid: number) => {
    try {
      process.kill(pid, 'SIGKILL');
    } catch {
      // already gone
    }
  };
  const groups = new Set(procs.filter((p) => sessions.has(p.session)).map((p) => p.pgid));
  for (const group of groups) kill(-group);
  for (const server of servers) kill(server.pid);
}

export default async function globalSetup(config?: FullConfig) {
  const originalEnvironment = { ...process.env };
  // Keep fixture commands, Playwright workers, and the managed Shelley server
  // independent of the developer's global/system git config and hooks.
  const restoreGitEnvironment = installHermeticGitEnvironment();

  const cleanup = () => {
    const dirs = tempDirs;
    tempDirs = [];
    try {
      killTerminalSessions(dirs);
      // Remove every directory even if one fails, then report the first error.
      const errors: unknown[] = [];
      for (const dir of dirs) {
        try {
          rmSync(dir, { recursive: true, force: true });
        } catch (error) {
          errors.push(error);
        }
      }
      if (errors.length > 0) throw errors[0];
    } finally {
      liveProvider?.close();
      liveProvider = null;
      delete process.env.SHELLEY_TEST_CWD;
      delete process.env[WORKER_SERVERS_ENV];
      restoreGitEnvironment();
    }
  };

  // test-results unless the config says otherwise; Playwright empties it
  // before global setup, so each run starts with fresh logs.
  const logDir = path.join(config?.projects[0]?.outputDir ?? path.join(__dirname, '..', 'test-results'), 'servers');

  try {
    return await startTestEnvironment(cleanup, originalEnvironment, config?.workers ?? 1, logDir);
  } catch (error) {
    // The servers must be gone before their directories are removed, and a
    // cleanup failure must not hide why setup failed.
    await Promise.allSettled(serverProcesses.map(({ process: server }) => {
      if (server.exitCode !== null || server.signalCode !== null) return;
      const exited = new Promise((resolve) => server.once('exit', resolve));
      server.kill('SIGKILL');
      return exited;
    }));
    serverProcesses = [];
    try {
      cleanup();
    } catch (cleanupError) {
      console.error('Cleanup after failed global setup also failed:', cleanupError);
    }
    throw error;
  }
}

async function startTestEnvironment(
  cleanup: () => void,
  originalEnvironment: NodeJS.ProcessEnv,
  workers: number,
  logDir: string,
) {
  // External servers keep their own environment; API fixtures still get a
  // small, real directory instead of the shared /tmp tree.
  if (process.env.TEST_SERVER_URL) {
    const env = makeEnvironmentDirs();
    process.env.SHELLEY_TEST_CWD = env.cwd;
    process.env.PLAYWRIGHT_TEST_BASE_URL = process.env.TEST_SERVER_URL;
    return cleanup;
  }

  // Building is not part of Git fixture isolation and may need the developer's
  // Git config for module fetching or version stamping.
  if (!existsSync(binPath)) {
    console.log('Building shelley binary…');
    execSync('go build -o bin/shelley ./cmd/shelley', {
      cwd: shelleyDir,
      stdio: 'inherit',
      env: originalEnvironment,
    });
  }

  const liveSessionURL = await startLiveProvider();

  // One Shelley server per worker. Tests used to share a single server, and
  // its single-writer SQLite DB and SSE fan-out were what capped the suite at
  // two workers per shard (a third caused systemic flake). With a server each,
  // a worker only ever contends with itself, so the worker count can follow
  // the cores available. They boot concurrently (their git fixtures are made
  // one after another, but that is quick).
  mkdirSync(logDir, { recursive: true });
  const servers = await Promise.all(
    Array.from({ length: Math.max(1, workers) }, (_, i) =>
      startServer(liveSessionURL, i, path.join(logDir, `${i}.log`))),
  );
  console.log(`Started ${servers.length} shelley test server(s); logs in ${path.relative(process.cwd(), logDir)}/`);

  // Workers select their server in playwright.config.ts, which Playwright
  // evaluates again in every worker process. The runner process (and callers
  // with no workers, like global-setup.test.ts) see the first one.
  process.env[WORKER_SERVERS_ENV] = JSON.stringify(servers);
  useWorkerServer(servers[0]);

  // Reap the servers before removing files they may still be writing.
  return async () => {
    const procs = serverProcesses;
    try {
      const results = await Promise.allSettled(procs.map((server) => stopServer(server)));
      const failed = results.find((r): r is PromiseRejectedResult => r.status === 'rejected');
      if (failed) throw failed.reason;
    } finally {
      serverProcesses = [];
      cleanup();
    }
  };
}

// Give every server its own home and cwd. The tests edit user AGENTS.md, and
// prompt hydration scans cwd for guidance/skills: sharing the runner's HOME or
// walking all of /tmp makes unrelated builds interfere.
function makeEnvironmentDirs() {
  const dir = mkdtempSync(path.join(tmpdir(), 'shelley-e2e-'));
  tempDirs.push(dir);
  const cwd = path.join(dir, 'cwd');
  const home = path.join(dir, 'home');
  mkdirSync(cwd);
  mkdirSync(home);
  return { dir, cwd, home };
}

async function startServer(liveSessionURL: string, index: number, logPath: string): Promise<WorkerServer> {
  const { dir, cwd, home } = makeEnvironmentDirs();

  // Git-viewer specs need real history, not the runner's giant checkout.
  const git = (...args: string[]) => execFileSync('git', [
    '-C', cwd, '-c', 'user.name=Shelley Test', '-c', 'user.email=test@example.com',
    '-c', 'commit.gpgsign=false', '-c', 'core.hooksPath=/dev/null', ...args,
  ], { env: hermeticGitEnvironment({ ...process.env, HOME: home }) });
  git('init', '--quiet', '--initial-branch=main');
  writeFileSync(path.join(cwd, 'example.txt'), 'before\n');
  git('add', 'example.txt');
  git('commit', '--quiet', '-m', 'Initial test fixture');
  writeFileSync(path.join(cwd, 'example.txt'), 'after\n');
  git('commit', '--quiet', '-am', 'Update test fixture');

  // Database and port file stay outside the conversation working directory.
  const db = path.join(dir, 'test.db');
  const portFile = path.join(dir, 'port');
  const socket = path.join(dir, 'client.sock');

  const name = `server ${index} (${path.relative(process.cwd(), logPath)})`;
  let earlyExit = false;
  let exitCode: number | null = null;
  // Its output, for a server that dies before it is up.
  const logTail = () => {
    try {
      return '\n' + readFileSync(logPath, 'utf8').trimEnd().split('\n').slice(-20).join('\n');
    } catch {
      return '';
    }
  };

  const log = openSync(logPath, 'a');

  const server = spawn(binPath, [
    '--predictable-only',
    '--db', db,
    'serve',
    '--port', '0',
    '--port-file', portFile,
    '--socket', socket,
  ], {
    cwd,
    stdio: ['ignore', log, log],
    env: hermeticGitEnvironment({
      ...process.env,
      HOME: home,
      PWD: cwd,
      PREDICTABLE_DELAY_MS: process.env.PREDICTABLE_DELAY_MS || '20',
      SHELLEY_LIVE_SESSION_URL: liveSessionURL,
      // Else a minute in, auto-upgrade swaps in the latest release and restarts.
      SHELLEY_SKIP_VERSION_CHECK: 'true',
    }),
  });
  // The child has its own copy of the descriptor.
  closeSync(log);
  serverProcesses.push({ process: server, name });

  server.on('exit', (code) => {
    earlyExit = true;
    exitCode = code;
  });

  // Wait for port file to appear (server has bound).
  const deadline = Date.now() + 30_000;
  while (!existsSync(portFile)) {
    if (earlyExit) {
      throw new Error(`Shelley ${name} exited (code ${exitCode}) before writing port file${logTail()}`);
    }
    if (Date.now() > deadline) {
      throw new Error(`Shelley ${name} did not write port file within 30s${logTail()}`);
    }
    await new Promise(r => setTimeout(r, 50));
  }

  const port = readFileSync(portFile, 'utf8').trim();
  const baseURL = `http://localhost:${port}`;

  // Wait for the server to actually respond to HTTP.
  const httpDeadline = Date.now() + 30_000;
  let httpReady = false;
  while (Date.now() < httpDeadline) {
    if (earlyExit) {
      throw new Error(`Shelley ${name} exited (code ${exitCode}) during startup${logTail()}`);
    }
    try {
      const res = await fetch(baseURL);
      if (res.ok) { httpReady = true; break; }
    } catch {
      // not ready yet
    }
    await new Promise(r => setTimeout(r, 100));
  }
  if (!httpReady) {
    throw new Error(`Shelley ${name} at ${baseURL} never responded OK within 30s${logTail()}`);
  }

  // Next prompt suggestions, on by default, would show up after every turn,
  // at a moment no test controls, and shift the layout under it. Their own
  // spec turns them on.
  const off = await fetch(`${baseURL}/feature-flags`, {
    method: 'POST',
    headers: { 'Content-Type': 'application/json' },
    body: JSON.stringify({ name: 'next-prompt-suggestions', value: false }),
  });
  if (!off.ok) {
    throw new Error(`Shelley ${name}: turning off next prompt suggestions: ${off.status} ${await off.text()}`);
  }

  return { baseURL, cwd, db, socket };
}

async function stopServer({ process: server, name }: ServerProcess) {
  if (server.exitCode !== null || server.signalCode !== null) {
    throw new Error(`Shelley ${name} exited before teardown (code ${server.exitCode}, signal ${server.signalCode})`);
  }
  const exited = new Promise<void>((resolve) => server.once('exit', () => resolve()));
  let timedOut = false;
  const deadline = setTimeout(() => {
    timedOut = true;
    server.kill('SIGKILL');
  }, 10_000);
  try {
    server.kill('SIGTERM');
    await exited;
    if (timedOut) throw new Error(`Shelley ${name} did not exit within 10s of SIGTERM`);
  } finally {
    clearTimeout(deadline);
  }
}
