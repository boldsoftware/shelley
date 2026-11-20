// Command secretrules generates ui/src/generated-secret-rules.ts, the
// composer's known-format secret rules (docs/secrets.md), from betterleaks'
// default configuration:
//
//	go run ./cmd/secretrules -o ui/src/generated-secret-rules.ts
//
// It keeps the provider-specific rules: not the generic ones (which flag
// ordinary assignments), component-only rules (IDs, URLs), or rules scoped
// to file paths. Go (RE2) regular expressions are translated to JavaScript
// through their parsed syntax tree, so case folding, classes and anchors
// keep their meaning; filters (Expr) are translated term by term, and a
// term this doesn't know fails the generation.
package main

import (
	"bytes"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"log"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"regexp/syntax"
	"strconv"
	"strings"
	"unicode"

	"github.com/BurntSushi/toml"
)

// The betterleaks release the rules come from.
const version = "v1.10.1"

const source = "https://raw.githubusercontent.com/betterleaks/betterleaks/" + version + "/config/betterleaks.toml"

// Rules left out: the generic ones match any key = value, password or
// URL with credentials, and box-api-access-token's filter reads the text
// around the match. The composer's own random-token rule covers random
// values more conservatively.
var skip = map[string]bool{
	"generic-api-key":        true,
	"generic-credential-uri": true,
	"generic-password":       true,
	"generic-username":       true,
	"box-api-access-token":   true,
	"kubernetes-secret-yaml": true,
}

// The longest kind the server accepts (server/secrets.go): a secret's ID is
// its kind and five characters, at most 64.
const maxKind = 59

type rule struct {
	ID          string   `toml:"id"`
	Regex       string   `toml:"regex"`
	Path        string   `toml:"path"`
	SecretGroup int      `toml:"secretGroup"`
	Keywords    []string `toml:"keywords"`
	Filter      string   `toml:"filter"`
	SkipReport  bool     `toml:"skipReport"`
}

type config struct {
	Filter string `toml:"filter"`
	Rules  []rule `toml:"rules"`
}

// The generated file's shapes, as JSON.
type jsAllow struct {
	Match     bool     `json:"match,omitempty"`
	Regexes   []string `json:"regexes,omitempty"`
	Stopwords []string `json:"stopwords,omitempty"`
}

type jsRule struct {
	ID          string    `json:"id"`
	Label       string    `json:"label"`
	Regex       string    `json:"regex"`
	Keywords    []string  `json:"keywords"`
	SecretGroup int       `json:"secretGroup,omitempty"`
	Entropy     float64   `json:"entropy,omitempty"`
	Wordy       bool      `json:"wordy,omitempty"`
	Require     []string  `json:"require,omitempty"`
	Allow       []jsAllow `json:"allow,omitempty"`
}

func main() {
	out := flag.String("o", "", "the TypeScript file to write")
	flag.Parse()
	if *out == "" {
		log.Fatal("usage: go run ./cmd/secretrules -o ui/src/generated-secret-rules.ts")
	}
	resp, err := http.Get(source)
	if err != nil {
		log.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		log.Fatalf("%s: %s", source, resp.Status)
	}
	data, err := io.ReadAll(resp.Body)
	if err != nil {
		log.Fatal(err)
	}
	src, err := generate(data)
	if err != nil {
		log.Fatal(err)
	}
	if err := os.WriteFile(*out, src, 0o644); err != nil {
		log.Fatal(err)
	}
	// Formatted like the rest of ui/src, so formatting it is no change.
	abs, err := filepath.Abs(*out)
	if err != nil {
		log.Fatal(err)
	}
	prettier := exec.Command("scripts/with-node", "pnpm", "--dir", "ui", "exec", "prettier", "--write", abs)
	prettier.Stdout, prettier.Stderr = os.Stderr, os.Stderr
	if err := prettier.Run(); err != nil {
		log.Fatalf("prettier: %v", err)
	}
}

func generate(data []byte) ([]byte, error) {
	var cfg config
	if err := toml.Unmarshal(data, &cfg); err != nil {
		return nil, err
	}
	global, err := parseFilter(cfg.Filter)
	if err != nil {
		return nil, fmt.Errorf("global filter: %w", err)
	}
	if global.entropy != 0 || global.wordy || len(global.require) > 0 || global.match.Regexes != nil {
		return nil, fmt.Errorf("global filter: only secret regexes and stopwords are supported")
	}
	var rules []jsRule
	for _, r := range cfg.Rules {
		if skip[r.ID] || r.SkipReport || r.Path != "" || r.Regex == "" {
			continue
		}
		// Variants of a rule are id.1, id.2: one kind.
		kind := regexp.MustCompile(`\.[0-9]+$`).ReplaceAllString(r.ID, "")
		if len(kind) > maxKind || !regexp.MustCompile(`^[a-z0-9]+(-[a-z0-9]+)*$`).MatchString(kind) {
			return nil, fmt.Errorf("rule id %q is not a valid secret kind", r.ID)
		}
		re, err := toJS(r.Regex, true)
		if err != nil {
			return nil, fmt.Errorf("%s: %w", r.ID, err)
		}
		f, err := parseFilter(r.Filter)
		if err != nil {
			return nil, fmt.Errorf("%s: %w", r.ID, err)
		}
		jr := jsRule{ID: kind, Label: label(kind), Regex: re, Keywords: lower(r.Keywords), SecretGroup: r.SecretGroup,
			Entropy: f.entropy, Wordy: f.wordy, Require: f.require}
		for _, a := range []jsAllow{f.secret, f.match} {
			if a.Regexes != nil || a.Stopwords != nil {
				jr.Allow = append(jr.Allow, a)
			}
		}
		rules = append(rules, jr)
	}
	var b bytes.Buffer
	fmt.Fprintf(&b, `// Known secret formats, from betterleaks %s (config/betterleaks.toml).
// Generated by cmd/secretrules; do not edit. Regenerate with:
//   go run ./cmd/secretrules -o ui/src/generated-secret-rules.ts
//
// Betterleaks is MIT licensed:
//
`, version)
	for _, line := range strings.Split(strings.TrimSpace(mitLicense), "\n") {
		fmt.Fprintf(&b, "//   %s\n", line)
	}
	b.WriteString(`
export interface KnownAllow {
  /** The regexes test the whole match, not the secret. */
  match?: boolean;
  regexes?: string[];
  stopwords?: string[];
}

export interface KnownRule {
  id: string;
  label: string;
  /** JavaScript source, for the flags "dgu". */
  regex: string;
  /** Lowercase; the rule runs only on text containing one. */
  keywords: string[];
  /** The capture group holding the secret; 0 means the first that matched,
   *  or the whole match. */
  secretGroup?: number;
  /** The secret's minimum Shannon entropy, in bits per character. */
  entropy?: number;
  /** Drop a secret made mostly of words. */
  wordy?: boolean;
  /** Regexes the secret must each match. */
  require?: string[];
  allow?: KnownAllow[];
}

`)
	for _, v := range []struct {
		decl string
		val  any
	}{{"export const KNOWN_ALLOW: KnownAllow = ", global.secret}, {"export const KNOWN_RULES: KnownRule[] = ", rules}} {
		j, err := json.MarshalIndent(v.val, "", "  ")
		if err != nil {
			return nil, err
		}
		fmt.Fprintf(&b, "%s%s;\n\n", v.decl, keyLike.ReplaceAllStringFunc(string(j), escapeFirst))
	}
	return bytes.TrimRight(b.Bytes(), "\n"), nil
}

// keyLike matches strings that look like live API keys to secret scanners
// (allowlists hold example keys, e.g. Google's published ones).
// They are written with their first character escaped, so the source holds
// no key-shaped literal and pushing it trips no scanner.
var keyLike = regexp.MustCompile(`(sk-|AIza|AKIA|gh[pousr]_|github_pat_|[sr]k_live_)[A-Za-z0-9_-]{16,}`)

func escapeFirst(s string) string { return fmt.Sprintf(`\u%04x`, s[0]) + s[1:] }

// filter is a translated filter (an Expr expression): a finding is dropped
// when any of its || terms holds.
type filter struct {
	entropy float64
	wordy   bool
	require []string
	secret  jsAllow
	match   jsAllow
}

var (
	entropyTerm = regexp.MustCompile(`^(?:filter\.)?entropy\(finding\["secret"\]\) <=? ([0-9.]+)$`)
	// The secret's length per BPE token: high for words. The composer
	// approximates it (secretDetect.ts, wordy).
	ratioTerm = regexp.MustCompile(`^(?:filter\.)?tokenRatio\(finding\["secret"\]\) >= 2\.5$`)
	listTerm  = regexp.MustCompile(`^(!?)(?:filter\.)?(matchesAny|containsAny)\((finding|attributes)\["(\w+)"\], \[([\s\S]*)\]\)$`)
)

func parseFilter(expr string) (filter, error) {
	var f filter
	terms, err := orTerms(expr)
	if err != nil {
		return f, err
	}
	for _, term := range terms {
		if m := entropyTerm.FindStringSubmatch(term); m != nil {
			if f.entropy, err = strconv.ParseFloat(m[1], 64); err != nil {
				return f, err
			}
			continue
		}
		if ratioTerm.MatchString(term) {
			f.wordy = true
			continue
		}
		m := listTerm.FindStringSubmatch(term)
		if m == nil {
			return f, fmt.Errorf("unsupported filter term %q", term)
		}
		not, fn, target := m[1] == "!", m[2], m[3]+"."+m[4]
		if target == "attributes.path" && !not {
			continue // typed text has no path
		}
		items, err := listItems(m[5])
		if err != nil {
			return f, fmt.Errorf("%q: %w", term, err)
		}
		switch {
		case not && fn == "matchesAny" && target == "finding.secret":
			for i, it := range items {
				items[i] = "(?:" + it + ")"
			}
			re, err := toJS(strings.Join(items, "|"), false)
			if err != nil {
				return f, err
			}
			f.require = append(f.require, re)
		case !not && fn == "matchesAny" && (target == "finding.secret" || target == "finding.match"):
			a := &f.secret
			if target == "finding.match" {
				a = &f.match
				a.Match = true
			}
			for _, it := range items {
				re, err := toJS(it, false)
				if err != nil {
					return f, err
				}
				a.Regexes = append(a.Regexes, re)
			}
		case !not && fn == "containsAny" && target == "finding.secret":
			f.secret.Stopwords = append(f.secret.Stopwords, lower(items)...)
		default:
			return f, fmt.Errorf("unsupported filter term %q", term)
		}
	}
	return f, nil
}

// orTerms splits an expression at its top-level ||, unwrapping
// parenthesized disjunctions.
func orTerms(expr string) ([]string, error) {
	expr = strings.TrimSpace(expr)
	if expr == "" {
		return nil, nil
	}
	var terms []string
	depth, start := 0, 0
	for i := 0; i < len(expr); i++ {
		switch c := expr[i]; c {
		case '`', '"':
			j := i + 1
			for ; j < len(expr) && expr[j] != c; j++ {
				if c == '"' && expr[j] == '\\' {
					j++
				}
			}
			if j >= len(expr) {
				return nil, fmt.Errorf("unterminated string in %q", expr)
			}
			i = j
		case '(', '[':
			depth++
		case ')', ']':
			depth--
		case '|':
			if depth == 0 && i+1 < len(expr) && expr[i+1] == '|' {
				terms = append(terms, strings.TrimSpace(expr[start:i]))
				start = i + 2
				i++
			}
		}
	}
	terms = append(terms, strings.TrimSpace(expr[start:]))
	if len(terms) == 1 && strings.HasPrefix(expr, "(") && strings.HasSuffix(expr, ")") {
		if inner, err := orTerms(expr[1 : len(expr)-1]); err == nil && len(inner) > 1 {
			return inner, nil
		}
	}
	var out []string
	for _, t := range terms {
		if len(terms) > 1 && strings.HasPrefix(t, "(") && strings.HasSuffix(t, ")") {
			inner, err := orTerms(t[1 : len(t)-1])
			if err != nil {
				return nil, err
			}
			if len(inner) > 1 {
				out = append(out, inner...)
				continue
			}
		}
		out = append(out, t)
	}
	return out, nil
}

// listItems parses a list's strings: `raw` or "quoted".
func listItems(list string) ([]string, error) {
	var items []string
	for s := strings.TrimSpace(list); s != ""; {
		var end int
		switch s[0] {
		case '`':
			end = strings.IndexByte(s[1:], '`') + 2
			if end == 1 {
				return nil, fmt.Errorf("unterminated string")
			}
			items = append(items, s[1:end-1])
		case '"':
			for end = 1; end < len(s) && s[end] != '"'; end++ {
				if s[end] == '\\' {
					end++
				}
			}
			end++
			if end > len(s) {
				return nil, fmt.Errorf("unterminated string")
			}
			it, err := strconv.Unquote(s[:end])
			if err != nil {
				return nil, err
			}
			items = append(items, it)
		default:
			return nil, fmt.Errorf("unexpected %q in list", s)
		}
		s = strings.TrimSpace(s[end:])
		s = strings.TrimSpace(strings.TrimPrefix(s, ","))
	}
	return items, nil
}

func lower(s []string) []string {
	out := make([]string, len(s))
	for i, w := range s {
		out[i] = strings.ToLower(w)
	}
	return out
}

var acronyms = map[string]string{
	"api": "API", "aws": "AWS", "gcp": "GCP", "jwt": "JWT", "pat": "PAT", "id": "ID", "url": "URL",
	"oauth": "OAuth", "npm": "npm", "pypi": "PyPI", "ci": "CI", "cicd": "CI/CD", "scim": "SCIM",
	"ca": "CA", "openai": "OpenAI", "github": "GitHub", "gitlab": "GitLab", "1password": "1Password",
	"digitalocean": "DigitalOcean", "huggingface": "Hugging Face", "tf": "Terraform", "ptt": "PTT",
	"rrt": "RRT", "ad": "AD", "pkcs12": "PKCS#12", "base64": "base64", "yaml": "YAML",
	"ai": "AI", "sdk": "SDK", "gemini": "Gemini", "xai": "xAI", "openrouter": "OpenRouter",
	"deepseek": "DeepSeek", "elevenlabs": "ElevenLabs", "togetherai": "Together AI", "voyageai": "Voyage AI",
	"nvidia": "NVIDIA", "langchain": "LangChain", "langsmith": "LangSmith", "posthog": "PostHog",
	"mongodb": "MongoDB", "dockerhub": "Docker Hub", "circleci": "CircleCI", "assemblyai": "AssemblyAI",
}

// label makes a rule id readable: github-pat is "GitHub PAT".
func label(id string) string {
	words := strings.Split(id, "-")
	for i, w := range words {
		if a, ok := acronyms[w]; ok {
			words[i] = a
		} else if i == 0 {
			words[i] = strings.ToUpper(w[:1]) + w[1:]
		}
	}
	return strings.Join(words, " ")
}

// toJS translates an RE2 regular expression into JavaScript source for the
// "u" flag, with the same matches. With trim, it drops leading optional
// runs, like the [\w.-]{0,50}? before a keyword (see trimLead).
func toJS(re string, trim bool) (string, error) {
	parsed, err := syntax.Parse(re, syntax.Perl)
	if err != nil {
		return "", err
	}
	if trim {
		trimLead(parsed)
	}
	var b strings.Builder
	if err := emit(&b, parsed); err != nil {
		return "", err
	}
	return b.String(), nil
}

// trimLead drops the optional runs a match starts with, in each branch of a
// leading alternation too: they only move where a match starts, and
// JavaScript's backtracking pays for them at every position (RE2 doesn't),
// making the keyword rules quadratic. Captures stay: they hold the secret.
func trimLead(re *syntax.Regexp) {
	switch re.Op {
	case syntax.OpAlternate:
		for _, sub := range re.Sub {
			trimLead(sub)
		}
	case syntax.OpConcat:
		for len(re.Sub) > 1 && optionalRun(re.Sub[0]) {
			re.Sub = re.Sub[1:]
		}
		trimLead(re.Sub[0])
	}
}

func optionalRun(re *syntax.Regexp) bool {
	switch re.Op {
	case syntax.OpStar, syntax.OpQuest:
	case syntax.OpRepeat:
		if re.Min != 0 {
			return false
		}
	default:
		return false
	}
	switch re.Sub[0].Op {
	case syntax.OpCharClass, syntax.OpAnyChar, syntax.OpAnyCharNotNL, syntax.OpLiteral:
		return true
	}
	return false
}

func emit(b *strings.Builder, re *syntax.Regexp) error {
	switch re.Op {
	case syntax.OpNoMatch:
		b.WriteString(`[^\s\S]`)
	case syntax.OpEmptyMatch:
	case syntax.OpLiteral:
		for _, r := range re.Rune {
			if re.Flags&syntax.FoldCase != 0 && unicode.SimpleFold(r) != r {
				b.WriteByte('[')
				for f := r; ; {
					classRune(b, f)
					if f = unicode.SimpleFold(f); f == r {
						break
					}
				}
				b.WriteByte(']')
			} else {
				literalRune(b, r)
			}
		}
	case syntax.OpCharClass:
		if len(re.Rune) == 2 && re.Rune[0] == 0 && re.Rune[1] == unicode.MaxRune {
			b.WriteString(`[\s\S]`)
			break
		}
		b.WriteByte('[')
		for i := 0; i < len(re.Rune); i += 2 {
			classRune(b, re.Rune[i])
			if re.Rune[i+1] != re.Rune[i] {
				b.WriteByte('-')
				classRune(b, re.Rune[i+1])
			}
		}
		b.WriteByte(']')
	case syntax.OpAnyCharNotNL:
		b.WriteString(`[^\n]`)
	case syntax.OpAnyChar:
		b.WriteString(`[\s\S]`)
	case syntax.OpBeginLine:
		b.WriteString(`(?<![^\n])`)
	case syntax.OpEndLine:
		b.WriteString(`(?![^\n])`)
	case syntax.OpBeginText:
		b.WriteString(`^`)
	case syntax.OpEndText:
		b.WriteString(`$`)
	case syntax.OpWordBoundary:
		b.WriteString(`\b`)
	case syntax.OpNoWordBoundary:
		b.WriteString(`\B`)
	case syntax.OpCapture:
		b.WriteByte('(')
		if err := emit(b, re.Sub[0]); err != nil {
			return err
		}
		b.WriteByte(')')
	case syntax.OpStar, syntax.OpPlus, syntax.OpQuest, syntax.OpRepeat:
		b.WriteString("(?:")
		if err := emit(b, re.Sub[0]); err != nil {
			return err
		}
		b.WriteByte(')')
		switch re.Op {
		case syntax.OpStar:
			b.WriteByte('*')
		case syntax.OpPlus:
			b.WriteByte('+')
		case syntax.OpQuest:
			b.WriteByte('?')
		case syntax.OpRepeat:
			switch {
			case re.Max == -1:
				fmt.Fprintf(b, "{%d,}", re.Min)
			case re.Max == re.Min:
				fmt.Fprintf(b, "{%d}", re.Min)
			default:
				fmt.Fprintf(b, "{%d,%d}", re.Min, re.Max)
			}
		}
		if re.Flags&syntax.NonGreedy != 0 {
			b.WriteByte('?')
		}
	case syntax.OpConcat:
		for _, sub := range re.Sub {
			if err := emit(b, sub); err != nil {
				return err
			}
		}
	case syntax.OpAlternate:
		b.WriteString("(?:")
		for i, sub := range re.Sub {
			if i > 0 {
				b.WriteByte('|')
			}
			if err := emit(b, sub); err != nil {
				return err
			}
		}
		b.WriteByte(')')
	default:
		return fmt.Errorf("unsupported regexp op %v", re.Op)
	}
	return nil
}

func literalRune(b *strings.Builder, r rune) {
	if strings.ContainsRune(`\^$.|?*+()[]{}/`, r) {
		b.WriteByte('\\')
		b.WriteRune(r)
		return
	}
	printable(b, r)
}

func classRune(b *strings.Builder, r rune) {
	if strings.ContainsRune(`\]^-[/`, r) {
		b.WriteByte('\\')
		b.WriteRune(r)
		return
	}
	printable(b, r)
}

func printable(b *strings.Builder, r rune) {
	if r > ' ' && r < 0x7f {
		b.WriteRune(r)
	} else {
		fmt.Fprintf(b, `\u{%x}`, r)
	}
}

const mitLicense = `
MIT License

Copyright (c) 2026 Zachary Rice

Permission is hereby granted, free of charge, to any person obtaining a copy
of this software and associated documentation files (the "Software"), to deal
in the Software without restriction, including without limitation the rights
to use, copy, modify, merge, publish, distribute, sublicense, and/or sell
copies of the Software, and to permit persons to whom the Software is
furnished to do so, subject to the following conditions:

The above copyright notice and this permission notice shall be included in all
copies or substantial portions of the Software.

THE SOFTWARE IS PROVIDED "AS IS", WITHOUT WARRANTY OF ANY KIND, EXPRESS OR
IMPLIED, INCLUDING BUT NOT LIMITED TO THE WARRANTIES OF MERCHANTABILITY,
FITNESS FOR A PARTICULAR PURPOSE AND NONINFRINGEMENT. IN NO EVENT SHALL THE
AUTHORS OR COPYRIGHT HOLDERS BE LIABLE FOR ANY CLAIM, DAMAGES OR OTHER
LIABILITY, WHETHER IN AN ACTION OF CONTRACT, TORT OR OTHERWISE, ARISING FROM,
OUT OF OR IN CONNECTION WITH THE SOFTWARE OR THE USE OR OTHER DEALINGS IN THE
SOFTWARE.
`
