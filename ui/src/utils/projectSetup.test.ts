import assert from "node:assert/strict";
import { test } from "node:test";
import { projectCwd, smsLink, uniqueProjectName, webAppsInstructions } from "./projectSetup";

const repo = (repository: string) => ({ integration: "gh", repository });

test("projectCwd follows the repositories on a VM", () => {
  assert.equal(projectCwd("Acme", [repo("acme/web")], false), "");
  assert.equal(projectCwd("Acme", [repo("acme/web")], true), "/home/exedev/web");
  assert.equal(projectCwd("Acme", [repo("acme/web"), repo("acme/api")], true), "/home/exedev");
  assert.equal(projectCwd("Acme Shop!", [], true), "/home/exedev/acme-shop");
  assert.equal(projectCwd("…", [], true), "/home/exedev/project");
});

test("uniqueProjectName numbers taken names", () => {
  assert.equal(uniqueProjectName("web", ["Self"]), "web");
  assert.equal(uniqueProjectName("web", ["Web", "web-2"]), "web-3");
});

test("webAppsInstructions reach VMs through the integration", () => {
  const text = webAppsInstructions("acme-exe", "acme-exe.int.exe.test", "NAME.exe.test");
  assert.match(text, /curl -X POST https:\/\/acme-exe\.int\.exe\.test\/exec/);
  assert.match(text, /SSH_AUTH_SOCK=\/tmp\/acme-exe\.sock ssh exedev@NAME\.exe\.test/);
  assert.match(text, /at https:\/\/NAME\.exe\.test\//);
  assert.doesNotMatch(webAppsInstructions("", "", ""), /exe-dev|curl/);
});

test("smsLink takes the body in the platform's form", () => {
  const iphone =
    "Mozilla/5.0 (iPhone; CPU iPhone OS 17_0 like Mac OS X) AppleWebKit/605.1.15 (KHTML, like Gecko) Version/17.0 Mobile/15E148 Safari/604.1";
  const mac =
    "Mozilla/5.0 (Macintosh; Intel Mac OS X 10_15_7) AppleWebKit/605.1.15 (KHTML, like Gecko) Version/17.0 Safari/605.1.15";
  const android =
    "Mozilla/5.0 (Linux; Android 14; Pixel 8) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/120.0 Mobile Safari/537.36";
  const body = "Hi there & more";
  assert.equal(smsLink("+1555", body, iphone), "sms:+1555&body=Hi%20there%20%26%20more");
  assert.equal(smsLink("+1555", body, mac), "sms:+1555&body=Hi%20there%20%26%20more");
  assert.equal(smsLink("+1555", body, android), "sms:+1555?body=Hi%20there%20%26%20more");
  assert.equal(smsLink("+1555", "Hi", ""), "sms:+1555?body=Hi");
});
