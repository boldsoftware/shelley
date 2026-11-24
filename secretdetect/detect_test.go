package secretdetect

import (
	"fmt"
	"regexp"
	"slices"
	"strings"
	"testing"
)

const (
	base62 = "ABCDEFGHIJKLMNOPQRSTUVWXYZabcdefghijklmnopqrstuvwxyz0123456789"
	hex    = "0123456789abcdef"
	lower  = "abcdefghijklmnopqrstuvwxyz0123456789"
	base32 = "ABCDEFGHIJKLMNOPQRSTUVWXYZ234567"
	base64 = base62 + "+/"
)

// rnd is n deterministic random characters from alphabet (base62 if
// none), mulberry32 as in the TypeScript tests.
func rnd(n int, alphabet ...string) string {
	abc := base62
	if len(alphabet) > 0 {
		abc = alphabet[0]
	}
	a := uint32(int64(n) * 2654435761)
	var b strings.Builder
	for range n {
		a += 0x6d2b79f5
		t := (a ^ a>>15) * (1 | a)
		t = (t + (t^t>>7)*(61|t)) ^ t
		b.WriteByte(abc[(t^t>>14)%uint32(len(abc))])
	}
	return b.String()
}

func base64Body(lines int) string {
	var out []string
	for i := range lines {
		out = append(out, rnd(64+i))
	}
	return strings.Join(out, "\n")
}

// kinds is what Detect finds in text, as kind and value pairs.
func kinds(text string) [][2]string {
	var out [][2]string
	for _, f := range Detect(text) {
		out = append(out, [2]string{f.Kind, f.Value})
	}
	return out
}

// kindsOf is kinds, of the kinds keep holds for.
func kindsOf(text string, keep func(kind string) bool) [][2]string {
	return slices.DeleteFunc(kinds(text), func(k [2]string) bool { return !keep(k[0]) })
}

func want(kind, value string) [][2]string { return [][2]string{{kind, value}} }

func check(t *testing.T, text string, got, want [][2]string) {
	t.Helper()
	if !slices.Equal(got, want) {
		t.Errorf("in %q: got %q, want %q", text, got, want)
	}
}

func TestKnownFormats(t *testing.T) {
	for _, tc := range [][2]string{
		{"openai-api-key", "sk-proj-" + rnd(74) + "T3BlbkFJ" + rnd(74)},
		{"anthropic-api-key", "sk-ant-api03-" + rnd(93) + "AA"},
		{"github-pat", "ghp_" + rnd(36)},
		{"github-fine-grained-pat", "github_pat_" + rnd(82)},
		{"gitlab-pat", "glpat-" + rnd(20)},
		{"aws-access-token", "AKIA" + rnd(16, base32)},
		{"slack-bot-token", "xoxb-1234567890-1234567890123-" + rnd(24)},
		{"stripe-access-token", "sk_live_" + rnd(24)},
		{"gcp-api-key", "AIza" + rnd(35)},
		{"npm-access-token", "npm_" + rnd(36)},
		{"sendgrid-api-token", "SG." + rnd(22) + "." + rnd(43)},
		{"jwt", "eyJhbGciOiJIUzI1NiJ9.eyJzdWIiOiIxMjM0NTY3ODkwIn0." + rnd(43)},
		{"token", "q8Zr3Kx0Vb7Nw2Lp5Tj9Hd4Fs6Gc1YmE4uW"},
		// Newer than gitleaks' rules.
		{"gcp-gemini-api", "AQ.Ab8RN6" + rnd(44)},
		{"groq-api-key", "gsk_" + rnd(52)},
		{"xai-api-key", "xai-" + rnd(80)},
		{"togetherai-api-key", "tgp_v1_" + rnd(43)},
		{"cerebras-api-key", "csk-" + rnd(48, lower)},
		{"supabase-management-token", "sbp_" + rnd(38, hex) + "42"},
		{"neon-api-key", "napi_" + rnd(64)},
		{"vercel-personal-access-token", "vcp_" + rnd(56)},
		{"buildkite-user-access-token", "bkua_" + rnd(40, lower)},
	} {
		text := `use "` + tc[1] + `" please`
		check(t, text, kinds(text), want(tc[0], tc[1]))
	}
	// Keyword rules take the value, not the name.
	heroku := "12345678-abcd-4ef0-9876-0123456789ab"
	check(t, "HEROKU_API_KEY="+heroku, kinds("HEROKU_API_KEY="+heroku), want("heroku-api-key", heroku))
	// A word-like value isn't a key (betterleaks' tokenRatio).
	vercel := rnd(24)
	check(t, "VERCEL_TOKEN="+vercel, kinds("VERCEL_TOKEN="+vercel), want("vercel-api-token", vercel))
	for _, text := range []string{
		"VERCEL_TOKEN=thisisnotarealvercelkey1",
		// betterleaks' own allowlist: AWS's documentation key.
		"AKIA" + "IOSFODNN7EXAMPLE",
	} {
		check(t, text, kinds(text), nil)
	}
}

func TestExeDevTokens(t *testing.T) {
	sig := "U1NIU0lHAAAAAQAAADMAAAALc3NoLWVkMjU1MTkAAAAg"
	for _, token := range []string{
		// exe1: crypto/rand.Text, 26 base32 characters.
		"exe1." + rnd(26, base32),
		"exe1." + rnd(20, base32),
		"exe1.QJ7XKZ4M2WNRTB5YHF3LDPVE6C",
		// exe0, as https-api-local-key.md makes it (a throwaway ed25519 key).
		"exe0.eyJleHAiOjE5MDAwMDAwMDB9.U1NIU0lHAAAAAQAAADMAAAALc3NoLWVkMjU1MTkAAAAgLSnPkYQ1fd0m0B3BAEOeqBw9hFjM2MvRTQkFHmI73bYAAAAKdjBAZXhlLmRldgAAAAAAAAAGc2hhNTEyAAAAUwAAAAtzc2gtZWQyNTUxOQAAAEBuKCM6A4kLDF8O29ee6AW50CyyFIJHZ1Vj4y8ek7VmDAXekCvxCY66HlFfQ9wX45tOuBMJ2AJ0ssnnvxKtPIgP",
		"exe0.e30." + sig + rnd(300),
		// The signature's longest.
		"exe0.e30.U1NIU0lH" + strings.Repeat(rnd(80), 100),
	} {
		for _, text := range []string{
			"use " + token + ".",
			`curl -X POST https://exe.dev/exec -H "Authorization: Bearer ` + token + `" -d whoami`,
		} {
			check(t, text, kinds(text), want("exe-dev-token", token))
		}
	}
	for _, text := range []string{
		`export TOKEN="exe0.$PAYLOAD.$SIGBLOB"`,
		`exe1 tokens are handles: ssh exe.dev exe0-to-exe1 "$TOKEN"`,
		"exe1.XXXXXXXXXXXXXXXXXXXXXXXXXX",
		"exe1.YOURTOKENGOESHEREPLEASE",
		"exe1." + rnd(19, base32),
		"exe1." + strings.ToLower(rnd(26, base32)),
		"exe0.e30." + rnd(300),
		"exe0.e30.U1NIU0lH" + strings.Repeat(rnd(80), 100) + "x",
		"xexe1." + rnd(26, base32),
		"exe1." + rnd(26, base32) + "-x",
	} {
		check(t, text, kindsOf(text, func(k string) bool { return k == "exe-dev-token" }), nil)
	}
}

func TestFormatsBetterleaksMisses(t *testing.T) {
	for _, tc := range [][3]string{
		{"gcp-gemini-api", "AQ.Ab8RN6" + rnd(44), "GEMINI_API_KEY=%"},
		{"gcp-api-key", "AIza" + rnd(35), "curl 'https://generativelanguage.googleapis.com/v1/models?key=%&alt=sse'"},
		{"anthropic-oauth-token", "sk-ant-oat01-" + rnd(93) + "AA", "CLAUDE_CODE_OAUTH_TOKEN=%"},
		{"tailscale-key", "tskey-auth-k" + rnd(12) + "CNTRL-" + rnd(33), "tailscale up --auth-key=%"},
		{"jina-ai-api-key", "jina_" + rnd(60), "use %"},
		{"fireworks-api-key", "fw_3" + rnd(23), "use %"},
		{"elevenlabs-api-key", "sk_" + rnd(48, hex), "use %"},
		{"cursor-api-key", "key_" + rnd(64, hex), "use %"},
		{"resend-api-key", "re_" + rnd(8) + "_" + rnd(24), "use %"},
		{"telegram-bot-api-token", "1234567890:AA" + rnd(33), "https://api.telegram.org/bot%/getMe"},
		{"telegram-bot-api-token", "1234567890:AA" + rnd(33), "TELEGRAM=%"},
		{"discord-webhook-url", "https://discord.com/api/webhooks/123456789012345678/" + rnd(68), "curl -d '{}' %"},
		{"database-password", rnd(20), "DATABASE_URL=postgres://app:%@db.example.com:5432/app"},
		{"database-password", rnd(12) + "!x", "redis-cli -u rediss://default:%@cache:6379"},
		{"aws-secret-access-key", rnd(30, base64) + "/" + rnd(9), "aws_secret_access_key = %"},
		{"aws-secret-access-key", rnd(30, base64) + "/" + rnd(9), `{"SecretAccessKey": "%"}`},
		{"railway-token", "8c1d5e4a-9b2f-4c3d-a1e6-7f0b2d9c4e81", "RAILWAY_TOKEN=%"},
		{"modal-token-secret", "as-" + rnd(22), "modal token set --token-id ak-x --token-secret %"},
	} {
		text := strings.Replace(tc[2], "%", tc[1], 1)
		check(t, text, kinds(text), want(tc[0], tc[1]))
	}
	for _, text := range []string{
		"postgres://postgres:postgres@localhost/db",
		"postgres://user:password@localhost/db",
		"postgres://user:${DB_PASSWORD}@db/app",
		"postgres://user:<password>@db/app",
		"postgres://localhost:5432/app",
		"aws_secret_access_key = wJalrXUtnFEMI/K7MDENG/bPxRfiCYEXAMPLEKEY",
		"the as-yet-unnamed modal dialog",
		"sk_" + strings.Repeat("0", 48),
		// Lookarounds: a key runs into what's around it.
		"xsk_" + rnd(48, hex),
		"sk_" + rnd(48, hex) + "-2",
		"x.AQ.Ab" + rnd(48),
		"curl 'https://example.com/v1/models?nokey=AIza" + rnd(35, hex) + "&alt=sse'",
		"https://api.telegram.org/x1234567890:AA" + rnd(33) + "/getMe",
		"10:1234567890:AA" + rnd(33),
		"https://discord.com/api/webhooks/123456789012345678/" + rnd(69),
		"xpostgres://app:" + rnd(20) + "@db",
		"aws_secret_access_key = " + rnd(30, base64) + "/" + rnd(9) + "=",
		"MY_RAILWAY_TOKEN=8c1d5e4a-9b2f-4c3d-a1e6-7f0b2d9c4e81",
		"modal token set --token-secret as-" + rnd(22) + "-x",
	} {
		check(t, text, kinds(text), nil)
	}
}

func TestValueNotName(t *testing.T) {
	token := "q8Zr3Kx0Vb7Nw2Lp5Tj9Hd4Fs6Gc1YmE4uW"
	for _, text := range []string{"SOME_ID=" + token, "x=" + token, "--auth=" + token} {
		check(t, text, kinds(text), want("token", token))
	}
	check(t, "TOKEN="+token+"==", kinds("TOKEN="+token+"=="), want("token", token+"=="))
	// Beside a name that says it's secret, even hex or one case.
	h := rnd(40, hex)
	lc := rnd(36, lower)
	for _, tc := range [][2]string{
		{"SOME_API_KEY=" + token, token},
		{"API_KEY=" + h, h},
		{`"client_secret": "` + lc + `"`, lc},
		{"DB_PASSWORD='" + strings.ToUpper(lc) + "'", strings.ToUpper(lc)},
	} {
		check(t, tc[0], kinds(tc[0]), want("named-secret", tc[1]))
	}
	for _, text := range []string{
		"commit " + h,
		"sha1=" + h,
		"cache_key=" + h,
		"memorySessionId=" + rnd(8, hex) + "-1234-4abc-8def-" + rnd(12, hex),
		"SECRET_KEY=thisisnotarealsecretkeyvalue12345",
		"API_KEY=" + h + "/x",
		"API_KEY=" + rnd(129, hex),
	} {
		check(t, text, kinds(text), nil)
	}
}

func TestOpenRouter(t *testing.T) {
	key := "sk-or-v1-" + rnd(64, hex)
	for _, text := range []string{"use " + key + " please", `export OPENROUTER_API_KEY="` + key + `"`} {
		check(t, text, kinds(text), want("openrouter-api-key", key))
	}
	for _, text := range []string{
		"export OPENROUTER_API_KEY=sk-or-v1-...",
		"keys start with sk-or-v1-",
		"sk-or-v1-" + strings.Repeat("0", 64),
		"sk-or-v1-" + rnd(63, hex),
		"npm i sk-or-v1-client",
	} {
		check(t, text, kinds(text), nil)
	}
}

func TestLuma(t *testing.T) {
	uuid := func() string {
		var parts []string
		for _, n := range []int{8, 4, 4, 4, 12} {
			parts = append(parts, rnd(n, hex))
		}
		return strings.Join(parts, "-")
	}
	// Luma AI: luma-api-… (Agents API), luma-… (Dream Machine); the body's
	// alphabet isn't documented.
	for _, key := range []string{
		"luma-api-" + rnd(43),
		"luma-api-" + rnd(30) + "-" + rnd(12) + "_" + rnd(9),
		"luma-api-" + rnd(32, lower),
		"luma-" + uuid() + "-" + uuid(),
		"luma-" + rnd(40, hex),
	} {
		for _, text := range []string{"use " + key + " please", `export LUMA_AGENTS_API_KEY="` + key + `"`} {
			check(t, text, kinds(text), want("luma-ai-api-key", key))
		}
	}
	// Luma events (luma.com): only beside its header or variable, or a
	// secret-… key in a paragraph about Luma.
	events := "secret-" + rnd(32)
	bare := rnd(30, lower)
	for _, tc := range [][2]string{
		{`curl -H "x-luma-api-key: ` + events + `" https://public-api.luma.com/v1/users/get-self`, events},
		{"set my luma api key to " + events, events},
		{"LUMA_API_KEY=" + bare, bare},
		{"luma_api_key: '" + bare + "'", bare},
		{"-H 'X-Luma-Api-Key: " + bare + "'", bare},
		{"LUMA_API_KEY=" + bare + `\n`, bare},
		// The header's value isn't one (a dot after), but the secret-… key
		// in it is: the rejected match is searched again from inside.
		{"x-luma-api-key: " + events + ".", events},
	} {
		check(t, tc[0], kindsOf(tc[0], func(k string) bool { return strings.HasPrefix(k, "luma") }), want("luma-api-key", tc[1]))
	}
	for _, text := range []string{
		// Placeholders from Luma's docs and examples.
		"curl -H 'authorization: Bearer luma-xxxx'",
		`curl -H "Authorization: Bearer luma-api-revoked_key_12345"`,
		`export LUMA_AGENTS_API_KEY="luma-api-..."`,
		`curl -H "x-luma-api-key: $LUMA_API_KEY" https://public-api.luma.com/v1/users/get-self`,
		`curl -H "x-luma-api-key: API_KEY_SECRET" https://api.lu.ma/user`,
		"export LUMA_API_KEY=your_luma_api_key_here",
		"LUMAAI_API_KEY=YOUR_LUMAAI_API_KEY_HERE",
		"luma auth set-key secret-XXXX",
		// Names.
		"see github.com/lumalabs/luma-agents-typescript and luma-agents-cli",
		"npm i luma-dream-machine-api-wrapper-v2",
		"the luma launch party is the secret-santa-gift-exchange-2024",
		// A secret- value away from any mention of Luma.
		"token secret-" + bare + " for the build",
		// Not a name ending in luma_api_key, nor a value running on.
		"NOTLUMA_API_KEY=" + bare,
		"LUMA_API_KEY=" + bare + ".x",
	} {
		// betterleaks' curl-auth-header takes any value in a curl header.
		check(t, text, kindsOf(text, func(k string) bool { return strings.HasPrefix(k, "luma") }), nil)
	}
}

func TestLongBase64URLTokens(t *testing.T) {
	// Base64url: a 154-character token has about five dashes and underscores.
	token := rnd(30) + "-" + rnd(31) + "_" + rnd(29) + "-" + rnd(33) + "__" + rnd(27)
	pat := "ghp_" + rnd(36)
	for _, text := range []string{
		"at " + token + ".",
		"use " + pat + " with " + token + " to reach it",
		"{{secret:private-key-f94d}} at " + token + "\n\nOnce",
		Marker("private-key-f94d") + " at " + token + "\n\nOnce",
	} {
		check(t, text, kindsOf(text, func(k string) bool { return k == "token" }), want("token", token))
	}
	// Names with many separators are not tokens.
	text := "run test_secrets_never_reach_the_model_v2_x9K3_q7Z1_a8B2_c4D5"
	check(t, text, kinds(text), nil)
}

func TestParagraphs(t *testing.T) {
	pat := "ghp_" + rnd(36)
	token := rnd(40)
	text := "use " + pat + "\n\nand " + token + " too"
	expect := []Finding{
		{Start: 4, End: 44, Value: pat, Kind: "github-pat"},
		{Start: 50, End: 90, Value: token, Kind: "token"},
	}
	if got := Detect(text); !slices.Equal(got, expect) {
		t.Errorf("Detect(%q) = %+v, want %+v", text, got, expect)
	}
	var starts []int
	for _, f := range Detect("use " + pat + "\n \n" + token) {
		starts = append(starts, f.Start)
	}
	if !slices.Equal(starts, []int{4, 47}) {
		t.Errorf("starts = %v, want [4 47]", starts)
	}
	// A rule's keyword in one paragraph does not make it run on another: a
	// secret-… value is Luma's only in a paragraph about Luma.
	luma := "secret-" + rnd(30, lower)
	text = "set up luma, then the build's token " + luma
	check(t, text, kinds(text), want("luma-api-key", luma))
	text = "set up luma\n\nthen the build's token " + luma
	check(t, text, kinds(text), nil)
}

func TestPrivateKeys(t *testing.T) {
	// PuTTY's Private-MAC line is at most 81 lines after its header.
	ppk := func(eol string, lines int) string {
		return "PuTTY-User-Key-File-3: ssh-ed25519" + eol + strings.Repeat("Comment: x"+eol, lines) + "Private-MAC: 9f8e7d6c"
	}
	for _, tc := range [][2]string{
		{"private-key", "-----BEGIN OPENSSH PRIVATE KEY-----\n" + base64Body(4) + "\n-----END OPENSSH PRIVATE KEY-----"},
		{"private-key", "-----BEGIN RSA PRIVATE KEY-----\n" + base64Body(4) + "\n-----END RSA PRIVATE KEY-----"},
		{"private-key", "-----BEGIN PGP PRIVATE KEY BLOCK-----\n\n" + base64Body(4) + "\n-----END PGP PRIVATE KEY BLOCK-----"},
		{"private-key", "-----BEGIN OPENSSH PRIVATE KEY-----\n" + base64Body(3)},
		{"putty-private-key", "PuTTY-User-Key-File-3: ssh-ed25519\nEncryption: none\nComment: me@box\nPublic-Lines: 1\n" + rnd(68) + "\nPrivate-Lines: 1\n" + rnd(44) + "\nPrivate-MAC: 9f8e7d6c5b4a39281706f5e4d3c2b1a0"},
		{"putty-private-key", ppk("\r\n", 80)},
		{"putty-private-key", "PuTTY-User-Key-File-2: x " + ppk("\n", 3)},
	} {
		text := "key:\n" + tc[1] + "\nthanks"
		check(t, text, kinds(text), want(tc[0], tc[1]))
	}
	for _, text := range []string{
		// A header alone is not a key.
		"the file starts with -----BEGIN OPENSSH PRIVATE KEY----- and so on",
		// Nor with too little after it.
		"-----BEGIN OPENSSH PRIVATE KEY-----\n" + rnd(60, hex) + "\n",
		ppk("\n", 81),
		ppk("\r\r\n", 3),
	} {
		check(t, text, kinds(text), nil)
	}
}

func TestKeywords(t *testing.T) {
	var tr trie
	var all []string
	for _, r := range knownRules {
		for _, k := range r.keywords {
			if !slices.Contains(all, k) {
				all = append(all, k)
				tr.add(k)
			}
		}
	}
	var upper []string
	for _, k := range slices.Backward(all) {
		upper = append(upper, strings.ToUpper(k))
	}
	// Overlapping and nested keywords, in a mix of cases.
	for _, text := range []string{
		"Sk_Live_ hf_ey xoxb-xoxp T3BlbkFJ sgp_ -----BEGIN PuTTY-User-Key-File",
		strings.Join(all, ""),
		strings.Join(upper, " "),
		rnd(5000, "abcdefghijklmnopqrstuvwxyz_-.3"),
	} {
		lower := asciiLower(text)
		seen := make([]int, len(tr.words))
		tr.mark(lower, func(k int) { seen[k]++ })
		for i, k := range tr.words {
			want := 0
			for j := range lower {
				if strings.HasPrefix(lower[j:], k) {
					want++
				}
			}
			if got := seen[i]; got != want {
				t.Errorf("keyword %q in %.40q: got %v, want %v", k, text, got, want)
			}
		}
	}
}

func TestOrdinaryText(t *testing.T) {
	for _, text := range []string{
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
		Marker("openai-key-k3f9") + " is already extracted",
		"import { defineComponent } from 'vue'; const x = useFeatureFlag('secrets');",
	} {
		check(t, text, kinds(text), nil)
	}
}

func TestMarkersStay(t *testing.T) {
	// IDs that look like a GitLab deploy token, and a secret beside its
	// name (secret:).
	id := "gldt-" + rnd(20, lower)
	check(t, id+"]", kinds(id+"]"), want("gitlab-deploy-token", id))
	named := rnd(40, lower)
	check(t, "secret:"+named+"}", kinds("secret:"+named+"}"), want("named-secret", named))
	pat := "ghp_" + rnd(36)
	for _, text := range []string{
		Marker(id),
		"{{secret:" + id + "}}",
		"{{secret:" + named + "}}",
		// A mark can't take a marker, nor a key a mark.
		"{{secret=" + Marker(id) + "}}",
		"{{secret=a {{secret:" + id + "}} b}}",
	} {
		check(t, text, kinds(text), nil)
		text += " " + pat
		check(t, text, kinds(text), want("github-pat", pat))
	}
}

func TestFindMarkers(t *testing.T) {
	m := Marker("openai-key-k3f9")
	text := "a " + m + " b {{secret:x-1}} {{secret:Bad}} [Secret extracted by Shelley; shelley secret -x prints the path of a file holding it]"
	p := strings.Index(text, "{{secret:x-1}}")
	got := findMarkers(text)
	want := []markerMatch{{2, 2 + len(m), "openai-key-k3f9"}, {p, p + len("{{secret:x-1}}"), "x-1"}}
	if !slices.Equal(got, want) {
		t.Errorf("findMarkers(%q) = %+v, want %+v", text, got, want)
	}
	if m != "[Secret extracted by Shelley; shelley secret openai-key-k3f9 prints the path of a file holding it]" {
		t.Errorf("Marker = %q", m)
	}
	if !regexp.MustCompile(`^` + IDPattern + `$`).MatchString("openai-key-k3f9") {
		t.Error("IDPattern doesn't match openai-key-k3f9")
	}
}

func TestManualMarks(t *testing.T) {
	if got, want := Detect("pw {{secret=hunter2}} ok"), []Finding{{Start: 3, End: 21, Value: "hunter2", Kind: "secret", Manual: true}}; !slices.Equal(got, want) {
		t.Errorf("got %+v, want %+v", got, want)
	}
	if got, want := Detect("{{secret:db-password=a b}c}}"), []Finding{{Start: 0, End: 28, Value: "a b}c", Kind: "secret", Name: "db-password", Manual: true}}; !slices.Equal(got, want) {
		t.Errorf("got %+v, want %+v", got, want)
	}
	// A key inside a mark is the mark's.
	pat := "ghp_" + rnd(36)
	check(t, "{{secret="+pat+"}}", kinds("{{secret="+pat+"}}"), want("secret", pat))
	check(t, "{{secret=multi\n\nline}}", kinds("{{secret=multi\n\nline}}"), want("secret", "multi\n\nline"))
	check(t, "{{secret=unclosed", kinds("{{secret=unclosed"), nil)
}

func TestKindLabel(t *testing.T) {
	for kind, want := range map[string]string{
		"openai-api-key": "OpenAI API key",
		"github-pat":     "GitHub PAT",
		"exe-dev-token":  "exe.dev token",
		"named-secret":   "Secret",
		"token":          "Random-looking token",
		"secret":         "Marked secret",
		"no-such-kind":   "no-such-kind",
	} {
		if got := KindLabel(kind); got != want {
			t.Errorf("KindLabel(%q) = %q, want %q", kind, got, want)
		}
	}
}

// Every generated regex compiles (compiled panics if not), and every rule
// with lookarounds skips to its prefixes, so a match they reject costs a
// check at its start, not a search.
func TestRulesCompile(t *testing.T) {
	d := compiled()
	if n := len(d.spanning) + len(d.local); n < 400 {
		t.Errorf("%d rules", n)
	}
	for _, r := range slices.Concat(d.spanning, d.local) {
		if (r.before != nil || r.after != nil || r.extend != nil) && r.starts.prefixes == nil {
			t.Errorf("%s has lookarounds but no prefixes", r.kind)
		}
	}
}

func FuzzDetect(f *testing.F) {
	f.Add("use ghp_" + rnd(36) + " and {{secret=x}} " + Marker("a-1") + "\n\n" + rnd(40))
	f.Fuzz(func(t *testing.T, text string) {
		end := 0
		for _, f := range Detect(text) {
			if f.Start < end || f.End <= f.Start || f.End > len(text) || !f.Manual && text[f.Start:f.End] != f.Value {
				t.Fatalf("bad finding %+v in %q", f, text)
			}
			end = f.End
		}
		// Rules find the same by skipping to their prefixes: those whose
		// keywords are in text, and one more.
		d := compiled()
		text = unfold(text)
		lower := asciiLower(text)
		var rules []*rule
		for i, r := range slices.Concat(d.spanning, d.local) {
			if i == len(text)%(len(d.spanning)+len(d.local)) ||
				slices.ContainsFunc(r.keywords, func(k int) bool { return strings.Contains(lower, d.keywords.words[k]) }) {
				rules = append(rules, r)
			}
		}
		got := d.candidates(text, lower, rules, false)
		want := d.candidates(text, "", rules, false)
		if !slices.Equal(got, want) {
			t.Fatalf("in %q: found %v by prefixes, want %v", text, got, want)
		}
	})
}

// corpus is about n bytes of prose, code, commands and logs, with a few
// secrets.
func corpus(n int) string {
	var b strings.Builder
	for i := 0; b.Len() < n; i++ {
		fmt.Fprintf(&b, `The deploy failed again last night, so I looked at the logs. The token refresh
runs every hour, but the key rotation job had a stale secret and the password
for the replica was wrong. Can you check the vault config and the storage
bucket policy? I think curl against the health endpoint returns 401 now.

`)
		fmt.Fprintf(&b, "```go\nfunc (s *Server) handle%d(w http.ResponseWriter, r *http.Request) {\n"+
			"\ttoken := r.Header.Get(\"Authorization\")\n\tif token == \"\" {\n\t\thttp.Error(w, \"no token\", http.StatusUnauthorized)\n\t\treturn\n\t}\n"+
			"\tkey, err := s.keys.Lookup(r.Context(), strings.TrimPrefix(token, \"Bearer \"))\n\tif err != nil {\n\t\tslog.ErrorContext(r.Context(), \"lookup\", \"err\", err)\n\t\treturn\n\t}\n"+
			"\tjson.NewEncoder(w).Encode(map[string]any{\"id\": key.ID, \"secret\": key.Redacted()})\n}\n```\n\n", i)
		fmt.Fprintf(&b, "```\n$ curl -sS -H \"Authorization: Bearer $API_TOKEN\" https://api.example.com/v1/items?page=%d\n"+
			"2026-11-21T10:%02d:13Z INFO request id=%s user=%s latency=12ms\n"+
			"commit %s\nexport DATABASE_URL=postgres://app:${DB_PASSWORD}@db.internal:5432/app\n```\n\n",
			i, i%60, rnd(32, hex), rnd(8, lower), rnd(40, hex))
		if i%8 == 0 {
			fmt.Fprintf(&b, "Here's the new one: OPENAI_API_KEY=sk-proj-%sT3BlbkFJ%s and the PAT ghp_%s.\n\n", rnd(74), rnd(74), rnd(36))
		}
	}
	return b.String()
}

// pathological is about n bytes of each text that once took Detect many
// times longer than prose.
func pathological(n int) []struct{ name, text string } {
	rep := func(s string) string { return strings.Repeat(s, n/len(s)) }
	return []struct{ name, text string }{
		// Work for each paragraph, or each paragraph with a keyword.
		{"blank-lines", rep("a\n\n")},
		{"newlines", rep("\n")},
		{"keyword-paragraphs", rep("key\n\n")},
		// A lookbehind rejecting each match, searched for again.
		{"rejected-lookbehind", rep("secret-") + "luma"},
		// Tries at each prefix reading the rest of the line.
		{"curl-line", rep("curl -u x ")},
		{"putty-line", rep("PuTTY-User-Key-File-2: ")},
		// A regex with 80 copies of a line.
		{"putty-headers", rep("PuTTY-User-Key-File-2: x\n")},
		// Kelvin sign: (?i)k matches it, so prefixes didn't.
		{"kelvin", "\u212a" + corpus(n)},
	}
}

func BenchmarkDetect(b *testing.B) {
	compiled()
	for _, bc := range append([]struct{ name, text string }{
		{"mixed", corpus(100 << 10)},
		// One paragraph: the regexes see all of it.
		{"mixed-one-paragraph", regexp.MustCompile(`\n\s*\n`).ReplaceAllString(corpus(100<<10), "\n")},
		{"base64", rnd(100<<10, base64)},
		{"message", corpus(1 << 10)},
	}, pathological(100<<10)...) {
		b.Run(bc.name, func(b *testing.B) {
			b.SetBytes(int64(len(bc.text)))
			for b.Loop() {
				Detect(bc.text)
			}
		})
	}
}
