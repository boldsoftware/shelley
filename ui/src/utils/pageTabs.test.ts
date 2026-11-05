import assert from "node:assert/strict";
import { test } from "node:test";
import type { Message } from "../types";
import { MAX_PAGE_TABS, frameURL, pageLinks, pageTabs, type PageLink } from "./pageTabs";

const agent = (...content: object[]): Message =>
  ({ type: "agent", llm_data: JSON.stringify({ Role: 1, Content: content }) }) as Message;

test("pageLinks finds declared and mentioned links in text and message_user", () => {
  const messages = [
    agent({ Type: 2, Text: 'See [Draft](https://hub.exe.dev/d/1 "tab") and https://x.dev/a.' }),
    agent({
      Type: 5,
      ToolName: "message_user",
      ToolInput: { text: '[Site](https://vm.exe.xyz:3000/ "tab")' },
    }),
    agent({ Type: 5, ToolName: "bash", ToolInput: { command: '[No](https://no.dev/ "tab")' } }),
    {
      type: "user",
      llm_data: JSON.stringify({ Content: [{ Type: 2, Text: '[U](https://u.dev/ "tab")' }] }),
    } as Message,
  ];
  assert.deepEqual(pageLinks(messages), [
    { title: "", url: "https://x.dev/a", declared: false },
    { title: "Draft", url: "https://hub.exe.dev/d/1", declared: true },
    { title: "Site", url: "https://vm.exe.xyz:3000/", declared: true },
  ]);
});

const link = (url: string, title = "", declared = !!title): PageLink => ({ url, title, declared });

test("a tab per origin follows the latest link there", () => {
  const tabs = pageTabs(
    [
      link("https://other.dev/x"), // not declared: no tab
      link("https://hub.exe.dev/deploy/a", "Draft"),
      link("https://github.com/o/r/pull/1", "PR"),
      link("https://hub.exe.dev/deploy/b"), // moves Draft, keeps its title
      link("https://vm.exe.xyz:3000/", "Site"),
      link("https://vm.exe.xyz:3000/next", "Next"), // moves and renames
    ],
    new Set(),
  );
  assert.deepEqual(tabs, [
    { key: "https://hub.exe.dev", url: "https://hub.exe.dev/deploy/b", title: "Draft", linked: 1 },
    { key: "https://github.com", url: "https://github.com/o/r/pull/1", title: "PR", linked: 1 },
    {
      key: "https://vm.exe.xyz:3000",
      url: "https://vm.exe.xyz:3000/next",
      title: "Next",
      linked: 1,
    },
  ]);
});

test("a closed tab returns when a link moves it; tabs are capped", () => {
  const links = [link("https://a.dev/1", "A")];
  assert.deepEqual(pageTabs(links, new Set(["https://a.dev/1"])), []);
  links.push(link("https://a.dev/2"));
  assert.equal(pageTabs(links, new Set(["https://a.dev/1"]))[0].url, "https://a.dev/2");
  const many = Array.from({ length: MAX_PAGE_TABS + 2 }, (_, i) =>
    link(`https://${i}.dev/`, `${i}`),
  );
  assert.deepEqual(
    pageTabs(many, new Set()).map((tab) => tab.title),
    many.slice(2).map((tab) => tab.title),
  );
});

test("a link again to a tab's page counts, so the page reloads", () => {
  const links = [
    link("https://a.dev/1", "A"),
    link("https://a.dev/1"),
    link("https://a.dev/1", "A"),
  ];
  assert.equal(pageTabs(links, new Set())[0].linked, 3);
  links.push(link("https://a.dev/2"));
  assert.equal(pageTabs(links, new Set())[0].linked, 1);
});

test("a reloaded frame's URL defeats the HTTP cache", () => {
  assert.equal(frameURL("https://a.dev/p?q=1#top", 0), "https://a.dev/p?q=1#top");
  assert.equal(
    frameURL("https://a.dev/p?q=1#top", 42),
    "https://a.dev/p?q=1&shelley_reload=42#top",
  );
});
