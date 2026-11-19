import assert from "node:assert/strict";
import { test } from "node:test";
import { GITLEAKS_RULES } from "../generated-secret-rules";
import {
  detectSecrets,
  keywordsIn,
  newSecretId,
  sealFindings,
  secretFiller,
  splitPlaceholders,
} from "./secretDetect";

const kinds = (text: string) => detectSecrets(text).map((f) => [f.kind, f.value]);

// Deterministic random characters from alphabet (mulberry32).
function rand(
  n: number,
  alphabet = "ABCDEFGHIJKLMNOPQRSTUVWXYZabcdefghijklmnopqrstuvwxyz0123456789",
): string {
  let a = n * 2654435761;
  let out = "";
  for (let i = 0; i < n; i++) {
    a = (a + 0x6d2b79f5) | 0;
    let t = Math.imul(a ^ (a >>> 15), 1 | a);
    t = (t + Math.imul(t ^ (t >>> 7), 61 | t)) ^ t;
    out += alphabet[((t ^ (t >>> 14)) >>> 0) % alphabet.length];
  }
  return out;
}
const base64Body = (lines: number) =>
  Array.from({ length: lines }, (_, i) => rand(64 + i)).join("\n");

test("finds known formats", () => {
  const keys: Array<[string, string]> = [
    ["openai-api-key", `sk-proj-${rand(74)}T3BlbkFJ${rand(74)}`],
    ["anthropic-api-key", `sk-ant-api03-${rand(93)}AA`],
    ["github-pat", `ghp_${rand(36)}`],
    ["github-fine-grained-pat", `github_pat_${rand(82)}`],
    ["gitlab-pat", `glpat-${rand(20)}`],
    ["aws-access-token", `AKIA${rand(16, "ABCDEFGHIJKLMNOPQRSTUVWXYZ234567")}`],
    ["slack-bot-token", `xoxb-1234567890-1234567890123-${rand(24)}`],
    ["stripe-access-token", `sk_live_${rand(24)}`],
    ["gcp-api-key", `AIza${rand(35)}`],
    ["npm-access-token", `npm_${rand(36)}`],
    ["sendgrid-api-token", `SG.${rand(22)}.${rand(43)}`],
    ["jwt", `eyJhbGciOiJIUzI1NiJ9.eyJzdWIiOiIxMjM0NTY3ODkwIn0.${rand(43)}`],
    ["token", "q8Zr3Kx0Vb7Nw2Lp5Tj9Hd4Fs6Gc1YmE4uW"],
  ];
  for (const [kind, value] of keys) {
    assert.deepEqual(kinds(`use "${value}" please`), [[kind, value]], value);
  }
  // Keyword rules take the value, not the name.
  const heroku = "12345678-abcd-4ef0-9876-0123456789ab";
  assert.deepEqual(kinds(`HEROKU_API_KEY=${heroku}`), [["heroku-api-key", heroku]]);
  // gitleaks' own allowlist: AWS's documentation key.
  assert.deepEqual(kinds("AKIA" + "IOSFODNN7EXAMPLE"), []);
});

test("finds exe.dev tokens", () => {
  const base32 = "ABCDEFGHIJKLMNOPQRSTUVWXYZ234567";
  for (const token of [
    // exe1: crypto/rand.Text, 26 base32 characters.
    `exe1.${rand(26, base32)}`,
    `exe1.${rand(20, base32)}`,
    "exe1.QJ7XKZ4M2WNRTB5YHF3LDPVE6C",
    // exe0, as https-api-local-key.md makes it (a throwaway ed25519 key).
    "exe0.eyJleHAiOjE5MDAwMDAwMDB9.U1NIU0lHAAAAAQAAADMAAAALc3NoLWVkMjU1MTkAAAAgLSnPkYQ1fd0m0B3BAEOeqBw9hFjM2MvRTQkFHmI73bYAAAAKdjBAZXhlLmRldgAAAAAAAAAGc2hhNTEyAAAAUwAAAAtzc2gtZWQyNTUxOQAAAEBuKCM6A4kLDF8O29ee6AW50CyyFIJHZ1Vj4y8ek7VmDAXekCvxCY66HlFfQ9wX45tOuBMJ2AJ0ssnnvxKtPIgP",
    `exe0.e30.U1NIU0lHAAAAAQAAADMAAAALc3NoLWVkMjU1MTkAAAAg${rand(300)}`,
  ]) {
    for (const text of [
      `use ${token}.`,
      `curl -X POST https://exe.dev/exec -H "Authorization: Bearer ${token}" -d whoami`,
    ]) {
      assert.deepEqual(kinds(text), [["exe-dev-token", token]], text);
    }
  }
  for (const text of [
    'export TOKEN="exe0.$PAYLOAD.$SIGBLOB"',
    'exe1 tokens are handles: ssh exe.dev exe0-to-exe1 "$TOKEN"',
    "exe1.XXXXXXXXXXXXXXXXXXXXXXXXXX",
    "exe1.YOURTOKENGOESHEREPLEASE",
    `exe1.${rand(19, base32)}`,
    `exe1.${rand(26, base32).toLowerCase()}`,
    `exe0.e30.${rand(300)}`,
  ]) {
    assert.deepEqual(
      kinds(text).filter(([kind]) => kind === "exe-dev-token"),
      [],
      text,
    );
  }
});

test("finds OpenRouter keys", () => {
  const key = `sk-or-v1-${rand(64, "0123456789abcdef")}`;
  for (const text of [`use ${key} please`, `export OPENROUTER_API_KEY="${key}"`]) {
    assert.deepEqual(kinds(text), [["openrouter-api-key", key]], text);
  }
  for (const text of [
    "export OPENROUTER_API_KEY=sk-or-v1-...",
    "keys start with sk-or-v1-",
    `sk-or-v1-${"0".repeat(64)}`,
    `sk-or-v1-${rand(63, "0123456789abcdef")}`,
    "npm i sk-or-v1-client",
  ]) {
    assert.deepEqual(kinds(text), [], text);
  }
});

test("finds Luma keys", () => {
  const hex = "0123456789abcdef";
  const lower = "abcdefghijklmnopqrstuvwxyz0123456789";
  const uuid = () => [8, 4, 4, 4, 12].map((n) => rand(n, hex)).join("-");
  // Luma AI: luma-api-… (Agents API), luma-… (Dream Machine); the body's
  // alphabet isn't documented.
  for (const key of [
    `luma-api-${rand(43)}`,
    `luma-api-${rand(30)}-${rand(12)}_${rand(9)}`,
    `luma-api-${rand(32, lower)}`,
    `luma-${uuid()}-${uuid()}`,
    `luma-${rand(40, hex)}`,
  ]) {
    for (const text of [`use ${key} please`, `export LUMA_AGENTS_API_KEY="${key}"`]) {
      assert.deepEqual(kinds(text), [["luma-ai-api-key", key]], text);
    }
  }
  // Luma events (luma.com): only beside its header or variable, or a
  // secret-… key in a paragraph about Luma.
  const events = `secret-${rand(32)}`;
  const bare = rand(30, lower);
  for (const [text, value] of [
    [`curl -H "x-luma-api-key: ${events}" https://public-api.luma.com/v1/users/get-self`, events],
    [`set my luma api key to ${events}`, events],
    [`LUMA_API_KEY=${bare}`, bare],
    [`luma_api_key: '${bare}'`, bare],
    [`-H 'X-Luma-Api-Key: ${bare}'`, bare],
  ]) {
    assert.deepEqual(kinds(text), [["luma-api-key", value]], text);
  }
  for (const text of [
    // Placeholders from Luma's docs and examples.
    "curl -H 'authorization: Bearer luma-xxxx'",
    'curl -H "Authorization: Bearer luma-api-revoked_key_12345"',
    'export LUMA_AGENTS_API_KEY="luma-api-..."',
    'curl -H "x-luma-api-key: $LUMA_API_KEY" https://public-api.luma.com/v1/users/get-self',
    'curl -H "x-luma-api-key: API_KEY_SECRET" https://api.lu.ma/user',
    "export LUMA_API_KEY=your_luma_api_key_here",
    "LUMAAI_API_KEY=YOUR_LUMAAI_API_KEY_HERE",
    "luma auth set-key secret-XXXX",
    // Names.
    "see github.com/lumalabs/luma-agents-typescript and luma-agents-cli",
    "npm i luma-dream-machine-api-wrapper-v2",
    "the luma launch party is the secret-santa-gift-exchange-2024",
    // A secret- value away from any mention of Luma.
    `token secret-${bare} for the build`,
  ]) {
    // gitleaks' curl-auth-header takes any value in a curl header.
    assert.deepEqual(
      kinds(text).filter(([kind]) => kind.startsWith("luma")),
      [],
      text,
    );
  }
});

test("finds long base64url tokens among other secrets", () => {
  // Base64url: a 154-character token has about five dashes and underscores.
  const token = `${rand(30)}-${rand(31)}_${rand(29)}-${rand(33)}__${rand(27)}`;
  const pat = `ghp_${rand(36)}`;
  for (const text of [
    `at ${token}.`,
    `use ${pat} with ${token} to reach it`,
    `{{secret:private-key-f94d}} at ${token}\n\nOnce`,
  ]) {
    assert.deepEqual(
      kinds(text).filter(([kind]) => kind === "token"),
      [["token", token]],
      text,
    );
  }
  // Names with many separators are not tokens.
  assert.deepEqual(kinds("run test_secrets_never_reach_the_model_v2_x9K3_q7Z1_a8B2_c4D5"), []);
});

test("paragraphs", () => {
  const pat = `ghp_${rand(36)}`;
  const token = rand(40);
  const text = `use ${pat}\n\nand ${token} too`;
  const want = [
    { start: 4, end: 44, value: pat, kind: "github-pat" },
    { start: 50, end: 90, value: token, kind: "token" },
  ];
  assert.deepEqual(detectSecrets(text), want);
  // Paragraphs seen before keep their findings, at their new offsets.
  assert.deepEqual(
    detectSecrets(`so, ${text}`),
    want.map((f) => ({ ...f, start: f.start + 4, end: f.end + 4 })),
  );
  assert.deepEqual(
    detectSecrets(`use ${pat}\n \n${token}`).map((f) => f.start),
    [4, 47],
  );
  // A rule's keyword in one paragraph does not make it run on another: a
  // commit hash is not a Sourcegraph token.
  assert.deepEqual(
    kinds("sgp_ tokens are Sourcegraph's\n\ncommit 9fceb02d0ae598e95dc970b74767f19372d61af8"),
    [],
  );
});

test("finds private keys", () => {
  for (const [kind, key] of [
    [
      "private-key",
      `-----BEGIN OPENSSH PRIVATE KEY-----\n${base64Body(4)}\n-----END OPENSSH PRIVATE KEY-----`,
    ],
    [
      "private-key",
      `-----BEGIN RSA PRIVATE KEY-----\n${base64Body(4)}\n-----END RSA PRIVATE KEY-----`,
    ],
    [
      "private-key",
      `-----BEGIN PGP PRIVATE KEY BLOCK-----\n\n${base64Body(4)}\n-----END PGP PRIVATE KEY BLOCK-----`,
    ],
    ["private-key", `-----BEGIN OPENSSH PRIVATE KEY-----\n${base64Body(3)}`],
    [
      "putty-private-key",
      `PuTTY-User-Key-File-3: ssh-ed25519\nEncryption: none\nComment: me@box\nPublic-Lines: 1\n${rand(68)}\nPrivate-Lines: 1\n${rand(44)}\nPrivate-MAC: 9f8e7d6c5b4a39281706f5e4d3c2b1a0`,
    ],
  ]) {
    assert.deepEqual(kinds(`key:\n${key}\nthanks`), [[kind, key]], key.slice(0, 40));
  }
  // A header alone is not a key.
  assert.deepEqual(kinds("the file starts with -----BEGIN OPENSSH PRIVATE KEY----- and so on"), []);
});

test("keywordsIn finds what includes does", () => {
  const all = [...new Set(GITLEAKS_RULES.flatMap((r) => r.keywords))];
  // Overlapping and nested keywords, in a mix of cases.
  const texts = [
    "Sk_Live_ hf_ey xoxb-xoxp T3BlbkFJ sgp_ -----BEGIN PuTTY-User-Key-File",
    all.join(""),
    all
      .map((k) => k.toUpperCase())
      .reverse()
      .join(" "),
    rand(5000, "abcdefghijklmnopqrstuvwxyz_-.3"),
  ];
  for (const text of texts) {
    const lower = text.toLowerCase();
    assert.deepEqual(
      [...keywordsIn(text)].filter((k) => all.includes(k)).sort(),
      all.filter((k) => lower.includes(k)).sort(),
    );
  }
});

test("leaves ordinary text alone", () => {
  for (const text of [
    "Refactor the sk-learn pipeline and the task-runner",
    "commit 9fceb02d0ae598e95dc970b74767f19372d61af8 broke it",
    "sha256:e3b0c44298fc1c149afbf4c8996fb92427ae41e4649b934ca495991b7852b855",
    "see https://example.com/a/B9cD3eF7gH1jK5mN8pQ2rS4tU6vW0xY/z",
    "open /home/user/src/AbstractSingletonProxyFactoryBean2024.java",
    "the test TestSecretsNeverReachTheModelOrTheLogs3 failed",
    "uuid 123e4567-e89b-12d3-a456-426614174000",
    "ghp_short is not a token",
    "AKIA is a prefix",
    "the heroku api key is in the vault; the twilio token too",
    "set password = hunter2 and api_key = changeme",
    "they say the key, eyJ is how JWTs start",
    "curl -u user:password https://example.com",
    "run make build-custom && go test ./server -run TestSecrets",
    "{{secret:openai-key-k3f9}} is already sealed",
    "import { defineComponent } from 'vue'; const x = useFeatureFlag('secrets');",
  ]) {
    assert.deepEqual(kinds(text), [], text);
  }
});

test("manual marks", () => {
  assert.deepEqual(detectSecrets("pw {{secret=hunter2}} ok"), [
    { start: 3, end: 21, value: "hunter2", kind: "secret", name: undefined, manual: true },
  ]);
  const [f] = detectSecrets("{{secret:db-password=a b}c}}");
  assert.equal(f.name, "db-password");
  assert.equal(f.value, "a b}c");
  // A key inside a mark is the mark's.
  assert.deepEqual(kinds(`{{secret=ghp_${rand(36)}}}`), [["secret", `ghp_${rand(36)}`]]);
  assert.deepEqual(kinds("{{secret=unclosed"), []);
});

test("ignored values stay", () => {
  const value = `ghp_${rand(36)}`;
  assert.deepEqual(detectSecrets(value, new Set([value])), []);
});

test("seal and split", () => {
  const text = `a ghp_${rand(36)} b {{secret=x}} c`;
  const findings = detectSecrets(text).map((f, i) => ({ ...f, id: `s-${i}` }));
  const sealed = sealFindings(text, findings);
  assert.equal(sealed, "a {{secret:s-0}} b {{secret:s-1}} c");
  assert.deepEqual(splitPlaceholders(sealed), [
    { text: "a " },
    { text: "{{secret:s-0}}", secretId: "s-0" },
    { text: " b " },
    { text: "{{secret:s-1}}", secretId: "s-1" },
    { text: " c" },
  ]);
});

test("ids and filler", () => {
  assert.equal(
    newSecretId("openai-key", () => 0),
    "openai-key-aaaa",
  );
  assert.match(newSecretId("token"), /^token-[a-z0-9]{4}$/);
  assert.equal(secretFiller("x-1234"), secretFiller("x-1234"));
  assert.notEqual(secretFiller("x-1234"), secretFiller("x-1235"));
});
