import { expect, type APIRequestContext, type Locator, type Page } from "@playwright/test";
import { execFileSync } from "node:child_process";
import { mkdtempSync, rmSync } from "node:fs";
import { tmpdir } from "node:os";
import { join } from "node:path";
import { deflateSync } from "node:zlib";
import { hermeticGitEnvironment } from "../scripts/git-test-env";

export async function openModelPicker(page: Page): Promise<Locator> {
  await expect(page.locator(".header")).toBeVisible();
  if (
    page.viewportSize()!.width < 768 &&
    !(await page.locator(".chat-overflow-popover").isVisible())
  ) {
    await page.getByRole("button", { name: "More options", exact: true }).click();
  }
  const picker = page.locator(".model-picker.p-select:visible");
  await picker.click();
  return picker;
}

export async function directoryControl(page: Page): Promise<Locator> {
  await expect(page.locator(".header")).toBeVisible();
  if (page.viewportSize()!.width >= 768) return page.locator(".status-field-cwd .status-chip");
  if (!(await page.locator(".chat-overflow-popover").isVisible())) {
    await page.getByRole("button", { name: "More options", exact: true }).click();
  }
  return page.locator(".chat-overflow-popover").getByRole("button", { name: /^Directory\b/ });
}

export async function openWorkspaceTool(page: Page, name: string): Promise<void> {
  // Phones keep workspace tools in the overflow menu, so the toolbar is
  // empty there.
  await expect(page.locator(".header")).toBeVisible();
  const button = page.locator(".chat-workspace-actions").getByRole("button", { name, exact: true });
  if (await button.isVisible()) {
    await button.click();
    return;
  }
  await page.getByRole("button", { name: "More options", exact: true }).click();
  await page
    .locator(".chat-overflow-popover")
    .getByRole("button", { name: new RegExp(`^${name.replace(/[.*+?^${}()|[\]\\]/g, "\\$&")}`) })
    .click();
}

export async function startNewConversationFromHeader(page: Page): Promise<void> {
  const button = page
    .locator(".header")
    .getByRole("button", { name: "New Conversation", exact: true });
  if (await button.isVisible()) {
    await button.click();
    return;
  }
  await page.getByRole("button", { name: "More options", exact: true }).click();
  await page
    .locator(".chat-overflow-popover")
    .getByRole("button", {
      name: "New Conversation",
      exact: true,
    })
    .click();
}

export function git(cwd: string, ...args: string[]): string {
  return execFileSync(
    "git",
    [
      "-c",
      "core.hooksPath=/dev/null",
      "-c",
      "commit.gpgsign=false",
      "-c",
      "user.name=Shelley Test",
      "-c",
      "user.email=test@example.com",
      ...args,
    ],
    { cwd, encoding: "utf8", env: hermeticGitEnvironment() },
  ).trim();
}

export function initGitRepo(cwd: string): void {
  git(cwd, "init");
  git(cwd, "config", "user.name", "Shelley Test");
  git(cwd, "config", "user.email", "test@example.com");
  git(cwd, "config", "commit.gpgsign", "false");
  git(cwd, "config", "core.hooksPath", "/dev/null");
}

// Set by globalSetup for both managed and externally supplied test servers.
// Do not default to /tmp: hydration would scan every other job's test files.
export function testWorkingDirectory(): string {
  const cwd = process.env.SHELLEY_TEST_CWD;
  if (!cwd) throw new Error("Playwright globalSetup did not set SHELLEY_TEST_CWD");
  return cwd;
}

export interface CreatedConversation {
  conversationId: string;
  slug: string;
}

interface CreateConversationOptions {
  agentTimeout?: number;
  cwd?: string;
  model?: string;
  conversationOptions?: Record<string, unknown>;
}

function sanitizeSlug(input: string): string {
  return input
    .toLowerCase()
    .replace(/[\s_]+/g, "-")
    .replace(/[^a-z0-9-]+/g, "")
    .replace(/-+/g, "-")
    .replace(/^-|-$/g, "")
    .slice(0, 60)
    .replace(/-$/g, "");
}

function buildStableTestSlug(currentSlug: string, conversationId: string): string {
  const uniqueSuffix = conversationId
    .replace(/[^a-z0-9]/gi, "")
    .toLowerCase()
    .slice(0, 8);
  const slugBase = sanitizeSlug(currentSlug) || "conversation";
  const maxBaseLength = Math.max(1, 60 - uniqueSuffix.length - 1);
  const truncatedBase = slugBase.slice(0, maxBaseLength).replace(/-$/g, "");
  return `${truncatedBase || "conversation"}-${uniqueSuffix}`;
}

async function renameConversationForTest(
  request: APIRequestContext,
  conversationId: string,
  currentSlug: string,
): Promise<string> {
  const desiredSlug = buildStableTestSlug(currentSlug, conversationId);
  const renameResp = await request.post(`/api/conversation/${conversationId}/rename`, {
    data: { slug: desiredSlug },
  });
  expect(renameResp.ok()).toBeTruthy();
  const renamedConversation = await renameResp.json();
  return renamedConversation.slug || desiredSlug;
}

/**
 * Poll a conversation until it has a slug. This is used for distillation flows
 * where there is no end_of_turn marker to wait on.
 */
export async function waitForConversationSlug(
  request: APIRequestContext,
  conversationId: string,
  timeout = 30000,
): Promise<string> {
  let slug = "";
  await expect(async () => {
    const resp = await request.get(`/api/conversation/${conversationId}`);
    expect(resp.ok()).toBeTruthy();
    const body = await resp.json();
    slug = body.conversation?.slug || "";
    expect(slug).toBeTruthy();
  }).toPass({ timeout });
  return slug;
}

/**
 * Rename a conversation to a stable unique test slug after background slug
 * generation has completed. This avoids collisions when many tests create
 * predictable-model conversations with the same prompts.
 */
export async function stabilizeConversationSlug(
  request: APIRequestContext,
  conversationId: string,
  currentSlug: string,
): Promise<string> {
  return renameConversationForTest(request, conversationId, currentSlug);
}

/**
 * Create a conversation via the API, wait for the agent to finish, then rename
 * it to a stable unique slug for deterministic direct navigation.
 *
 * This avoids two sources of flake:
 * 1. The SSE subscribe-vs-publish race when the browser opens a brand new
 *    conversation while the first turn is still being recorded.
 * 2. Slug collisions when many predictable-model tests create similar prompts.
 */
export async function createConversationViaAPIWithDetails(
  request: APIRequestContext,
  message: string,
  opts: CreateConversationOptions = {},
): Promise<CreatedConversation> {
  const {
    agentTimeout = 30000,
    cwd = testWorkingDirectory(),
    model = "predictable",
    conversationOptions,
  } = opts;
  const newResp = await request.post("/api/conversations/new", {
    data: { message, model, cwd, conversation_options: conversationOptions },
  });
  expect(newResp.ok()).toBeTruthy();
  const { conversation_id: conversationId } = await newResp.json();

  let currentSlug = "";
  await expect(async () => {
    const resp = await request.get(`/api/conversation/${conversationId}`);
    expect(resp.ok()).toBeTruthy();
    const body = await resp.json();
    const done = body.messages?.some(
      (m: { type: string; end_of_turn?: boolean }) => m.type === "agent" && m.end_of_turn === true,
    );
    expect(done).toBeTruthy();
    currentSlug = body.conversation?.slug || "";
    expect(currentSlug).toBeTruthy();
  }).toPass({ timeout: agentTimeout });

  const slug = await stabilizeConversationSlug(request, conversationId, currentSlug);
  return { conversationId, slug };
}

export async function createConversationViaAPI(
  request: APIRequestContext,
  message: string,
  opts: CreateConversationOptions = {},
): Promise<string> {
  const { slug } = await createConversationViaAPIWithDetails(request, message, opts);
  return slug;
}

/** Completed tool cards far from the viewport render as cheap geometry
 *  placeholders until they scroll near it (see composables/nearViewport.ts).
 *  Printing reveals them all at once, which is how a spec that needs every
 *  card in a long conversation mounted gets there without scrolling (and
 *  without a sleep): the reveal is synchronous in the page. */
export async function mountAllToolCards(page: Page): Promise<void> {
  await page.evaluate(() => window.dispatchEvent(new Event("beforeprint")));
  await expect(page.locator(".tool-card-mount-placeholder")).toHaveCount(0, { timeout: 15000 });
}

/** Override a boolean feature flag for THIS page only (via localStorage).
 *  Call before the first `page.goto(...)` so the override is in place when
 *  the React app first reads the flag. Per-page scope means parallel
 *  workers can disagree on the same flag without racing on the global DB. */
export async function setPageFeatureFlag(page: Page, name: string, value: boolean): Promise<void> {
  await page.addInitScript(
    ([n, v]) => {
      try {
        window.localStorage.setItem(`ff:${n}`, String(v));
      } catch {
        /* localStorage unavailable; flag will fall back to server default */
      }
    },
    [name, value] as const,
  );
}

/** End the terminals running in `cwd`. Every page load replays every running
 *  terminal on the shared server, so ones left running slow later tests. */
export async function closeTerminalsIn(request: APIRequestContext, cwd: string): Promise<void> {
  const terminals: Array<{ id: string; cwd: string }> = await (
    await request.get("/api/terminals")
  ).json();
  for (const { id } of terminals.filter((terminal) => terminal.cwd === cwd)) {
    expect((await request.delete(`/api/terminals/${id}`)).ok()).toBe(true);
  }
}

/** Choose `item` from the terminal panel's menu. */
export async function chooseTerminalMenuItem(page: Page, item: string): Promise<void> {
  await page.getByRole("button", { name: "Terminal menu", exact: true }).click();
  await page
    .locator(".overflow-menu-panel")
    .getByRole("button", { name: item, exact: true })
    .click();
}

/** Have terminals on this page render with `renderer`, as if chosen in the
 *  terminal panel's menu. Call before the first `page.goto(...)`. */
export async function setPageTerminalRenderer(
  page: Page,
  renderer: "ghostty" | "xterm",
): Promise<void> {
  await page.addInitScript((r) => localStorage.setItem("shelley-terminal-renderer", r), renderer);
}

/** Run `fn` with a fresh temp directory, removing it afterwards. Specs that
 *  need a real cwd with real files on disk (file finder, patch cards) use this
 *  so a full suite run doesn't litter /tmp. */
export async function withTempDir(
  prefix: string,
  fn: (dir: string) => Promise<void>,
): Promise<void> {
  const dir = mkdtempSync(join(tmpdir(), prefix));
  try {
    await fn(dir);
  } finally {
    rmSync(dir, { recursive: true, force: true });
  }
}

// Send a real editing transaction so Tiptap removes atomic filter chips too.
export async function clearConversationQuery(search: Locator): Promise<void> {
  await search.focus();
  await search.press("ControlOrMeta+A");
  await search.press("Backspace");
  await expect(search).toHaveAttribute("data-query-value", "");
}

/** A minimal valid RGB PNG of the given size, filled with a gradient. */
export function makePNG(width: number, height: number): Buffer {
  const raw = Buffer.alloc(height * (1 + width * 3));
  for (let y = 0; y < height; y++) {
    const row = y * (1 + width * 3);
    for (let x = 0; x < width; x++) {
      const p = row + 1 + x * 3;
      raw[p] = (x * 7) & 0xff;
      raw[p + 1] = (y * 3) & 0xff;
      raw[p + 2] = (x + y) & 0xff;
    }
  }
  const ihdr = Buffer.alloc(13);
  ihdr.writeUInt32BE(width, 0);
  ihdr.writeUInt32BE(height, 4);
  ihdr[8] = 8; // bit depth
  ihdr[9] = 2; // color type: truecolor
  return Buffer.concat([
    Buffer.from([0x89, 0x50, 0x4e, 0x47, 0x0d, 0x0a, 0x1a, 0x0a]),
    chunk("IHDR", ihdr),
    chunk("IDAT", deflateSync(raw, { level: 1 })),
    chunk("IEND", Buffer.alloc(0)),
  ]);
}

function chunk(type: string, data: Buffer): Buffer {
  const body = Buffer.concat([Buffer.from(type, "ascii"), data]);
  const len = Buffer.alloc(4);
  len.writeUInt32BE(data.length);
  const crc = Buffer.alloc(4);
  crc.writeUInt32BE(crc32(body));
  return Buffer.concat([len, body, crc]);
}

const CRC_TABLE = (() => {
  const table = new Int32Array(256);
  for (let n = 0; n < 256; n++) {
    let c = n;
    for (let k = 0; k < 8; k++) c = c & 1 ? 0xedb88320 ^ (c >>> 1) : c >>> 1;
    table[n] = c;
  }
  return table;
})();

function crc32(buf: Buffer): number {
  let c = -1;
  for (let i = 0; i < buf.length; i++) c = CRC_TABLE[(c ^ buf[i]) & 0xff] ^ (c >>> 8);
  return (c ^ -1) >>> 0;
}

// Recording needs a transcription route, which the predictable-only test
// server never has; specs that record mock the chat endpoint instead.
export async function installTranscriptionAvailability(page: Page, available: boolean) {
  await page.addInitScript((transcriptionAvailable) => {
    let init: Record<string, unknown> | undefined;
    Object.defineProperty(window, "__SHELLEY_INIT__", {
      configurable: true,
      get: () => init,
      set: (value) => {
        init = { ...value, transcription_available: transcriptionAvailable };
      },
    });
  }, available);
}

// Keep link ordering deliberate: the VM link is not the first configured link.
export async function installServerLinks(page: Page, includeVM = true) {
  const links = [
    { title: "Manage on exe.dev", url: "https://exe.example/vm/vm" },
    ...(includeVM ? [{ title: "vm.example", url: "https://vm.example/website" }] : []),
    { title: "Documentation", url: "https://docs.example/documentation" },
  ];
  await page.addInitScript((serverLinks) => {
    let init: Record<string, unknown> | undefined;
    Object.defineProperty(window, "__SHELLEY_INIT__", {
      configurable: true,
      get: () => init,
      set: (value) => {
        init = { ...value, hostname: "vm.example", links: serverLinks };
      },
    });
  }, links);
  return links;
}
