package secretdetect

import (
	"regexp"
	"strings"
	"unicode/utf8"
)

// Providers betterleaks misses, or takes only beside the provider's name.
// They run before betterleaks, whose generic rules (curl-auth-header) would
// take their keys under a vaguer kind.
var providerRules = []ruleSpec{
	// exe.dev (sshkey/token.go in exe): exe0.PAYLOAD.SIGBLOB, base64url, the
	// signature an SSHSIG blob; exe1. and 20 to 40 base32 characters (26 from
	// crypto/rand.Text). A digit, so exe1.YOURTOKENGOESHERE isn't one; misses
	// 1 in 220.
	provider("exe-dev-token", "exe.dev token", `exe0\.[A-Za-z0-9_-]{2,}\.U1NIU0lH[A-Za-z0-9_-]{64,}`, []string{"exe0."},
		wordDash, nil, nil).around(nil, func(t string, m []int) bool {
		// RE2 repeats at most 1000 times: the signature's 8000 is here.
		sig := strings.LastIndex(t[:m[1]], ".U1NIU0lH") + len(".U1NIU0lH")
		return m[1]-sig <= 8000 && notBefore(wordDash)(t, m)
	}),
	provider("exe-dev-token", "exe.dev token", `exe1\.[A-Z2-7]{20,40}`, []string{"exe1."}, wordDash, wordDash,
		func(s string) bool { return strings.ContainsAny(s, "234567") }),
	// Luma AI (lumalabs.ai): luma-api-… (Agents API), luma-… (Dream Machine).
	provider("luma-ai-api-key", "Luma AI API key", `luma-(?:api-)?[A-Za-z0-9_-]{20,200}`, []string{"luma-"}, wordDash, wordDash,
		func(s string) bool { return notName(lumaPrefix.ReplaceAllString(s, "")) }),
	// Luma events (luma.com): the format isn't documented (keys are said to
	// start with secret-, too common alone), so only a value given as the
	// x-luma-api-key header or a LUMA*_API_KEY, or a secret-… key in a
	// paragraph about Luma.
	provider("luma-api-key", "Luma API key",
		"(?i)(?:x-luma-api-key|luma(?:ai|_agents)?_api_key)[\"'`]?[ \\t]{0,3}(?::=|=>|[:=])[ \\t\"'`=]{0,5}([A-Za-z0-9_-]{16,200})|(secret-[A-Za-z0-9_-]{16,200})",
		[]string{"luma"}, nil, nil,
		func(s string) bool { return notName(secretPrefix.ReplaceAllString(s, "")) }).around(
		func(t string, start int) bool {
			// The first letter says which alternative: secret-… isn't in
			// a word, nor \bluma…_api_key (x-luma-api-key may be).
			switch t[start] | 0x20 {
			case 's':
				return notAfter(wordDash)(t, start)
			case 'l':
				return notAfter(word)(t, start)
			}
			return true
		},
		func(t string, m []int) bool {
			if m[2] < 0 {
				return notBefore(wordDash)(t, m)
			}
			// A name's value ends at a quote, space, ;,)} or \n, or the end.
			c, _ := utf8.DecodeRuneInString(t[m[1]:])
			return m[1] == len(t) || jsSpace(c) || strings.ContainsRune("\"'`;,)}", c) ||
				c == '\\' && m[1]+1 < len(t) && (t[m[1]+1] == 'n' || t[m[1]+1] == 'r')
		},
	),
	// Gemini: AI Studio makes only AQ. keys since May 2026, 53 characters;
	// betterleaks wants AQ.Ab8RN6 and a quote or space after.
	provider("gcp-gemini-api", "Gemini API key", `AQ\.Ab[A-Za-z0-9_-]{48}`, []string{"aq.ab"}, wordDotDash, wordDash, nil),
	// A Google API key in a URL (?key=AIza…&alt=sse), which betterleaks
	// skips: it wants a quote or space after.
	provider("gcp-api-key", "GCP API key", `AIza[\w-]{35}`, []string{"aiza"}, nil, wordDash, nil).around(
		func(t string, start int) bool {
			return start >= 5 && t[start-4:start] == "key=" && (t[start-5] == '?' || t[start-5] == '&')
		}, nil,
	),
	// Claude Code's OAuth tokens (claude setup-token) and refresh tokens.
	provider("anthropic-oauth-token", "Anthropic OAuth token", `sk-ant-o[ar]t01-[\w-]{80,200}`, []string{"sk-ant-o"}, wordDash, wordDash, nil),
	// Tailscale (tailscale.com/docs/reference/key-prefixes): tskey-TYPE-,
	// the key's ID, CNTRL- and the secret.
	provider("tailscale-key", "Tailscale key", `tskey-(?:[a-z]+-)?k[A-Za-z0-9]{4,16}CNTRL-[A-Za-z0-9]{20,64}`, []string{"tskey-"}, wordDash, wordDash, nil),
	provider("jina-ai-api-key", "Jina AI API key", `jina_[\w-]{60}`, []string{"jina_"}, wordDash, wordDash, nil),
	// Fireworks AI (trufflehog's detector): fw_ and 20 to 40 base62.
	provider("fireworks-api-key", "Fireworks API key", `fw_[A-Za-z0-9]{20,40}`, []string{"fw_"}, wordDash, wordDash, mixed),
	// Formats betterleaks takes only beside the provider's name.
	provider("elevenlabs-api-key", "ElevenLabs API key", `sk_[0-9a-f]{48}`, []string{"sk_"}, wordDash, wordDash, nil),
	provider("cursor-api-key", "Cursor API key", `key_[0-9a-f]{64}`, []string{"key_"}, wordDash, wordDash, nil),
	provider("resend-api-key", "Resend API key", `re_[A-Za-z0-9]{8}_[A-Za-z0-9]{24}`, []string{"re_"}, wordDash, wordDash, nil),
	// Telegram: also in its API's URLs, /botTOKEN/getMe.
	provider("telegram-bot-api-token", "Telegram bot API token", `[0-9]{8,10}:AA[\w-]{33}`, []string{":aa"}, nil, wordDash, nil).around(
		func(t string, start int) bool {
			return strings.HasSuffix(t[:start], "/bot") || notAfter(wordColon)(t, start)
		}, nil,
	),
	provider("discord-webhook-url", "Discord webhook URL",
		`https://(?:(?:ptb|canary)\.)?discord(?:app)?\.com/api/webhooks/[0-9]{17,20}/[\w-]{60,68}`, []string{"discord"},
		nil, wordDash, nil),
	// A password in a database URL (postgres://user:PASSWORD@host), alone, so
	// the URL stays readable; not a placeholder or a word, like
	// postgres:postgres@localhost. MongoDB's are betterleaks'.
	provider("database-password", "Database password",
		"(?i)(?:postgres(?:ql)?|mysql|mariadb|rediss?|amqps?)://[^"+jsSpaces+":/@'\"`]{0,128}:([^"+jsSpaces+"/@'\"`]{6,256})@[\\w.[]",
		[]string{"postgres", "mysql", "mariadb", "redis", "amqp"}, word, nil,
		func(s string) bool { return !strings.ContainsAny(s, "$%{}<>*") && notName(s) }),
	// An AWS secret access key beside its name (credentials file, env,
	// JSON): its / and + keep it from the random-token rule. Not AWS's
	// documentation key.
	assigned("aws-secret-access-key", "AWS secret access key", `secret_?access_?key`, `[A-Za-z0-9/+]{40}`,
		[]string{"secret_access_key", "secretaccesskey"}, nil,
		func(s string) bool { return !strings.HasSuffix(s, "EXAMPLEKEY") }),
	// Railway tokens are UUIDs, and Modal's token secrets as-…: only beside
	// their names.
	assigned("railway-token", "Railway token", `RAILWAY_(?:API_)?TOKEN`, `[0-9a-f]{8}(?:-[0-9a-f]{4}){3}-[0-9a-f]{12}`,
		[]string{"railway_"}, word, nil),
	provider("modal-token-secret", "Modal token secret", `as-[A-Za-z0-9]{16,32}`, []string{"modal"}, wordDash, wordDash,
		func(s string) bool { return strings.ContainsAny(s, "0123456789") && notName(s[3:]) }),
}

var (
	lumaPrefix   = regexp.MustCompile(`^luma-(?:api-)?`)
	secretPrefix = regexp.MustCompile(`^(?i)secret-`)
)

// A key beside a name that says it's secret (API_KEY=…, "client_secret":
// "…"), even of hex or one case, which the random-token rule skips (hashes,
// IDs).
var namedSecret = assigned("named-secret", "Secret", `(?:api[_-]?key|secret|token|passw(?:or)?d)`, `[A-Za-z0-9]{32,128}`,
	[]string{"key", "secret", "token", "passw"}, nil,
	func(s string) bool { return strings.ContainsAny(s, "0123456789") && !wordy(s) })

// Formats betterleaks misses: a private key pasted without its END line
// (taken to the end of its base64 lines, which are long or end in
// padding), and PuTTY's .ppk files. They run after betterleaks, whose
// complete private keys win.
var shelleyRules = []ruleSpec{
	{
		kind:     "private-key",
		label:    "Private key",
		regex:    `-----BEGIN[ A-Z0-9_-]{0,100}PRIVATE KEY(?: BLOCK)?-----(?:\r?\n[A-Za-z-]+:[^\r\n]*)*(?:(?:\r?\n)+(?:[A-Za-z0-9+/]{16,}={0,2}|[A-Za-z0-9+/]*=+))+`,
		keywords: []string{"-----begin"},
		// 64 base64 characters, besides the BEGIN line.
		ok: func(s string) bool {
			return len(base64Char.FindAllStringIndex(pemHeader.ReplaceAllString(s, ""), 64)) == 64
		},
	},
	{
		kind:  "putty-private-key",
		label: "PuTTY private key",
		// Its header line, to the first Private-MAC line in the 81
		// after: (?:\r?\n[^\r\n]*){0,80}?\r?\nPrivate-MAC: [0-9a-fA-F]+ is
		// 80 copies in RE2, slow on every line.
		regex:    `PuTTY-User-Key-File-[23]: [^\r\n]*`,
		keywords: []string{"putty-user-key-file"},
		before:   firstPuTTY,
		extend:   puttyMAC,
	},
}

// firstPuTTY reports whether no header comes before start on its line. If
// one does, the header at start has the same lines after: it's in that
// header's match, or there's no match. Searching back to the header
// before it, not to the line start, takes linear time in all of them.
func firstPuTTY(t string, start int) bool {
	const header = "PuTTY-User-Key-File-"
	for i := start - 1; i >= 0; i-- {
		switch t[i] {
		case '\r', '\n':
			return true
		case ' ':
			if i >= len(header)+2 && t[i-1] == ':' && (t[i-2] == '2' || t[i-2] == '3') && t[i-len(header)-2:i-2] == header {
				return false
			}
		}
	}
	return true
}

// puttyMAC is the end of the hex run of the Private-MAC line within 81
// lines after end, or -1.
func puttyMAC(t string, end int) int {
	for range 81 {
		// \r?\n
		if strings.HasPrefix(t[end:], "\r") {
			end++
		}
		if !strings.HasPrefix(t[end:], "\n") {
			return -1
		}
		end++
		if mac, ok := strings.CutPrefix(t[end:], "Private-MAC: "); ok {
			if n := len(mac) - len(strings.TrimLeft(mac, "0123456789abcdefABCDEF")); n > 0 {
				return len(t) - len(mac) + n
			}
		}
		// [^\r\n]*
		if i := strings.IndexAny(t[end:], "\r\n"); i >= 0 {
			end += i
		} else {
			return -1
		}
	}
	return -1
}

var (
	pemHeader  = regexp.MustCompile(`^-----[^\n]*-----`)
	base64Char = regexp.MustCompile(`[A-Za-z0-9+/=]`)
)

// provider is a rule of ours: the secret is the first capture group that
// matched, or the whole match, of at least 3 bits per character. A match
// is not right after one of before nor right before one of after, if
// they're not nil.
func provider(kind, label, regex string, keywords []string, before, after *chars, ok func(string) bool) ruleSpec {
	s := ruleSpec{kind: kind, label: label, regex: regex, keywords: keywords, entropy: 3, ok: ok}
	if before != nil {
		s.before = notAfter(before)
	}
	if after != nil {
		s.after = notBefore(after)
	}
	return s
}

// around is s with the checks before or after that aren't nil.
func (s ruleSpec) around(before func(string, int) bool, after func(string, []int) bool) ruleSpec {
	if before != nil {
		s.before = before
	}
	if after != nil {
		s.after = after
	}
	return s
}

// assigned is a provider rule for a value assigned to a name: NAME=VALUE,
// NAME: "VALUE", "NAME": "VALUE"; case-insensitive, and not right after
// one of before.
func assigned(kind, label, name, value string, keywords []string, before *chars, ok func(string) bool) ruleSpec {
	re := "(?i)" + name + "[\"'`]?[ \\t]{0,3}(?::=|=>|[:=])[ \\t\"'`]{0,5}(" + value + ")"
	return provider(kind, label, re, keywords, before, wordAssign, ok)
}

// chars is a set of ASCII characters.
type chars [utf8.RuneSelf]bool

// wordAnd is \w and extra.
func wordAnd(extra string) *chars {
	var c chars
	for _, r := range "ABCDEFGHIJKLMNOPQRSTUVWXYZabcdefghijklmnopqrstuvwxyz0123456789_" + extra {
		c[r] = true
	}
	return &c
}

var (
	word        = wordAnd("")
	wordDash    = wordAnd("-")
	wordDotDash = wordAnd(".-")
	wordColon   = wordAnd(":")
	wordAssign  = wordAnd("+/=-")
)

// notAfter reports whether a match starting at start isn't right after
// one of c: (?<![c]).
func notAfter(c *chars) func(string, int) bool {
	return func(t string, start int) bool {
		return start == 0 || t[start-1] >= utf8.RuneSelf || !c[t[start-1]]
	}
}

// notBefore reports whether a match isn't right before one of c: (?![c]).
func notBefore(c *chars) func(string, []int) bool {
	return func(t string, m []int) bool {
		return m[1] == len(t) || t[m[1]] >= utf8.RuneSelf || !c[t[m[1]]]
	}
}

// jsSpaces is JavaScript's \s, which takes Unicode spaces RE2's doesn't.
const jsSpaces = `\t\n\v\f\r \x{a0}\x{1680}\x{2000}-\x{200a}\x{2028}\x{2029}\x{202f}\x{205f}\x{3000}\x{feff}`

func jsSpace(c rune) bool {
	return strings.ContainsRune("\t\n\v\f\r \u00a0\u1680\u2028\u2029\u202f\u205f\u3000\ufeff", c) || 0x2000 <= c && c <= 0x200a
}
