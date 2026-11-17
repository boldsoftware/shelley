import { once } from "node:events";
import { createServer, type ServerResponse } from "node:http";
import {
  test,
  expect,
  type APIRequestContext,
  type APIResponse,
  type Page,
} from "@playwright/test";
import type { ConversationWithState } from "../src/types";
import { createConversationViaAPIWithDetails } from "./helpers";

async function writeEvent(response: ServerResponse, event: unknown): Promise<void> {
  await new Promise<void>((resolve, reject) => {
    response.write(`data: ${JSON.stringify(event)}\n\n`, (error) =>
      error ? reject(error) : resolve(),
    );
  });
}

function conversationMessages(body: unknown) {
  return (
    (
      body as {
        messages?: Array<{
          message_id: string;
          type: string;
          end_of_turn?: boolean;
          llm_data?: string | null;
        }>;
      }
    ).messages ?? []
  );
}

async function latestCompletedReply(request: APIRequestContext, conversationId: string) {
  let reply:
    | {
        message_id: string;
        text: string;
      }
    | undefined;
  await expect(async () => {
    const response = await request.get(`/api/conversation/${conversationId}`);
    expect(response.ok()).toBeTruthy();
    const messages = conversationMessages(await response.json());
    const completed = messages.filter(
      (message) => message.type === "agent" && message.end_of_turn === true,
    );
    expect(completed.length).toBeGreaterThanOrEqual(2);
    const last = completed.at(-1)!;
    const llm = JSON.parse(last.llm_data || "{}") as {
      Content?: Array<{ Type: number; Text?: string }>;
    };
    const text = (llm.Content ?? [])
      .filter((part) => part.Type === 2)
      .map((part) => part.Text ?? "")
      .join("");
    expect(text).not.toBe("");
    reply = { message_id: last.message_id, text };
  }).toPass({ timeout: 30000 });
  return reply!;
}

async function snapshotConversations(request: APIRequestContext) {
  const response = await request.get("/api/conversations/snapshot");
  expect(response.ok()).toBeTruthy();
  return (await response.json()).conversations as ConversationWithState[];
}

async function sendWorking(
  page: Page,
  response: ServerResponse,
  conversations: ConversationWithState[],
  conversationId: string,
  working: boolean,
) {
  const hash = `assistant-preview-${working}-${crypto.randomUUID()}`;
  await writeEvent(response, {
    conversation_list_patch: {
      reset: true,
      new_hash: hash,
      at: new Date().toISOString(),
      patch: [
        {
          op: "replace",
          path: "",
          value: conversations.map((conversation) =>
            conversation.conversation_id === conversationId
              ? { ...conversation, working, agent_working: working }
              : conversation,
          ),
        },
      ],
    },
  });
  await page.waitForFunction(
    (hash) => (window as Window & { __previewPatch?: string }).__previewPatch === hash,
    hash,
  );
  await page.evaluate(
    () =>
      new Promise<void>((resolve) =>
        requestAnimationFrame(() => requestAnimationFrame(() => resolve())),
      ),
  );
}

async function waitForText(page: Page, text: string) {
  await page.waitForFunction(
    (needle) => document.querySelector(".messages-container")?.textContent?.includes(needle),
    text,
    { timeout: 15000 },
  );
}

test("REST catch-up retires a streamed answer before the next send", async ({
  page,
  request,
  baseURL,
}) => {
  const url = baseURL ? new URL(baseURL) : undefined;
  test.skip(
    !url || url.protocol !== "http:" || !["localhost", "127.0.0.1", "[::1]"].includes(url.hostname),
    "Controlled SSE fixture requires a local HTTP baseURL.",
  );

  const conversation = await createConversationViaAPIWithDetails(request, "Hello");
  await page.addInitScript(() => {
    const NativeEventSource = window.EventSource;
    window.EventSource = class extends NativeEventSource {
      constructor(url: string | URL, options?: EventSourceInit) {
        super(url, options);
        this.addEventListener("message", (event) => {
          const data = JSON.parse(event.data);
          if (data.conversation_list_patch) {
            (window as Window & { __previewPatch?: string }).__previewPatch =
              data.conversation_list_patch.new_hash;
          }
        });
      }
    };
  });
  const responses: ServerResponse[] = [];
  const server = createServer((_request, response) => {
    responses.push(response);
    response.writeHead(200, {
      "Content-Type": "text/event-stream",
      "Access-Control-Allow-Origin": "*",
    });
    response.flushHeaders();
  });
  server.listen(0, "127.0.0.1");
  await once(server, "listening");
  const address = server.address();
  if (!address || typeof address === "string") throw new Error("Missing SSE server port");
  await page.route("**/api/stream2*", (route) =>
    route.continue({ url: `http://127.0.0.1:${address.port}/` }),
  );

  let releaseChatResponse!: () => void;
  const chatResponseGate = new Promise<void>((resolve) => {
    releaseChatResponse = resolve;
  });
  let chatStarted!: () => void;
  const chatStartedPromise = new Promise<void>((resolve) => {
    chatStarted = resolve;
  });
  let chatResponse: APIResponse | undefined;

  try {
    await page.goto(`/c/${conversation.slug}`);
    await expect(page.getByTestId("message-input")).toBeVisible({ timeout: 30000 });
    await waitForText(page, "Hello! I'm Shelley, your AI assistant.");
    await expect.poll(() => responses.length).toBe(1);

    // The real predictable-model reply is persisted by the server, but the
    // controlled EventSource deliberately receives only its partial delta.
    const accepted = await request.post(`/api/conversation/${conversation.conversationId}/chat`, {
      data: { message: "echo: answer lost on reconnect", model: "predictable" },
    });
    expect(accepted.ok()).toBeTruthy();
    const reply = await latestCompletedReply(request, conversation.conversationId);
    await sendWorking(
      page,
      responses[0],
      await snapshotConversations(request),
      conversation.conversationId,
      true,
    );
    await writeEvent(responses[0], {
      conversation_id: conversation.conversationId,
      stream_delta: {
        type: "text",
        text: reply.text,
        index: 1,
        seq: 0,
        message_id: reply.message_id,
      },
    });
    await expect(page.locator(".streaming-message")).toContainText(reply.text);
    await sendWorking(
      page,
      responses[0],
      await snapshotConversations(request),
      conversation.conversationId,
      false,
    );
    await expect(page.locator(".streaming-message")).toBeHidden();

    // Disconnect after the final persisted row was omitted. EventSource
    // reconnects; its successful open asks ChatInterface to REST-catch-up.
    const catchupRequest = page.waitForRequest(
      (candidate) =>
        candidate.method() === "GET" &&
        candidate
          .url()
          .includes(`/api/conversation/${conversation.conversationId}?last_sequence_id=`),
      { timeout: 30000 },
    );
    responses[0].end();
    await expect.poll(() => responses.length, { timeout: 30000 }).toBe(2);
    await catchupRequest;
    await waitForText(page, reply.text);
    await expect(page.locator(".streaming-message")).toBeHidden();

    // Let the actual chat POST reach the server (which starts the next turn),
    // but withhold its HTTP response. The realistic working=true list patch
    // makes ChatInterface synchronize transient state while that send is in
    // flight; no new-turn stream delta is delivered by this controlled SSE.
    await page.route(`**/api/conversation/${conversation.conversationId}/chat`, async (route) => {
      chatResponse = await route.fetch();
      chatStarted();
      await chatResponseGate;
      await route.fulfill({ response: chatResponse });
    });
    await page.getByTestId("message-input").fill("echo: next turn");
    await page.getByTestId("send-button").click();
    await chatStartedPromise;
    await sendWorking(
      page,
      responses[1],
      await snapshotConversations(request),
      conversation.conversationId,
      true,
    );

    // The previous answer must stay only in its durable message row; it must
    // not flash back into the live streaming-preview element.
    await expect(page.locator(".streaming-message")).toBeHidden();
  } finally {
    releaseChatResponse();
    for (const response of responses) response.end();
    await new Promise<void>((resolve, reject) => {
      server.close((error) => (error ? reject(error) : resolve()));
      server.closeAllConnections();
    });
  }
});
