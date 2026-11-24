// Package secretdetect finds secrets in text (docs/secrets.md): known
// formats (betterleaks' rules, generated into generated_rules.go, and a few
// of our own), a conservative rule for long random tokens, and the manual
// {{secret=VALUE}} and {{secret:NAME=VALUE}} marks. False negatives beat
// mangled prose: the marks cover what this misses.
package secretdetect

import (
	"cmp"
	"math"
	"regexp"
	"slices"
	"strings"
	"sync"
	"unicode/utf8"
)

// A Finding is a secret in a text. Start and End are byte offsets.
type Finding struct {
	Start, End int
	Value      string // the secret; for a manual mark, the value inside it
	Kind       string // rule id, e.g. "openai-api-key", "token", "secret" (manual)
	Name       string // the ID a manual {{secret:NAME=VALUE}} mark asks for
	Manual     bool   // a manual mark; its span covers the whole mark
}

// IDPattern matches a secret's ID, e.g. openai-key-k3f9.
const IDPattern = `[a-z0-9]+(?:-[a-z0-9]+)*`

// The marker says `shelley secret ID` prints a path: models otherwise take
// its output for the value. No $ or backticks, which markdown would parse.
const (
	markerPrefix = "[Secret extracted by Shelley; shelley secret "
	markerSuffix = " prints the path of a file holding it]"
)

// Marker is the text that replaces an extracted secret.
func Marker(id string) string { return markerPrefix + id + markerSuffix }

// A markerMatch is a marker or legacy {{secret:ID}} placeholder.
type markerMatch struct {
	start, end int
	id         string
}

// findMarkers finds the markers and legacy placeholders in text, in order.
func findMarkers(text string) []markerMatch {
	var out []markerMatch
	// Markers can't overlap placeholders, and two searches that each skip
	// to a literal prefix are much faster than one for either.
	for _, re := range []*regexp.Regexp{markerRE, placeholderRE} {
		for _, m := range re.FindAllStringSubmatchIndex(text, -1) {
			out = append(out, markerMatch{m[0], m[1], text[m[2]:m[3]]})
		}
	}
	slices.SortFunc(out, func(a, b markerMatch) int { return cmp.Compare(a.start, b.start) })
	return out
}

var (
	markerRE      = regexp.MustCompile(regexp.QuoteMeta(markerPrefix) + `(` + IDPattern + `)` + regexp.QuoteMeta(markerSuffix))
	placeholderRE = regexp.MustCompile(`\{\{secret:(` + IDPattern + `)\}\}`)
)

var markRE = regexp.MustCompile(`\{\{secret(?::(` + IDPattern + `))?=((?s:.+?))\}\}`)

// KindLabel is a kind's human label, e.g. "OpenAI API key" for
// "openai-api-key", or the kind itself.
func KindLabel(kind string) string {
	if l, ok := compiled().labels[kind]; ok {
		return l
	}
	return kind
}

// Detect finds secrets in text, in order, without overlaps. Markers and
// legacy placeholders are left alone.
func Detect(text string) []Finding {
	d := compiled()
	var found findings
	for _, m := range findMarkers(text) {
		found.add(Finding{Start: m.start, End: m.end})
	}
	// Marks are explicit: they win over everything.
	for _, m := range markRE.FindAllStringSubmatchIndex(text, -1) {
		f := Finding{Start: m[0], End: m[1], Value: text[m[4]:m[5]], Kind: "secret", Manual: true}
		if m[2] >= 0 {
			f.Name = text[m[2]:m[3]]
		}
		found.add(f)
	}
	subject := unfold(text)
	lower := asciiLower(subject)
	// Only private keys span paragraphs (PEM and PGP blocks hold blank
	// lines); everything else is found within one, where its keyword is.
	if slices.ContainsFunc(d.spanningKeywords, func(k string) bool { return strings.Contains(lower, k) }) {
		for _, f := range d.candidates(subject, lower, d.spanning, false) {
			found.add(f)
		}
	}
	// Per paragraph: the rules its keywords pick, in order. seen and queued
	// hold the paragraph a keyword was last seen in, and a rule queued for.
	seen := make([]int, len(d.keywords.words))
	queued := make([]int, len(d.local))
	var queue []int
	var rules []*rule
	para := 0
	paragraphs(subject, func(start, end int) {
		para++
		queue = queue[:0]
		d.keywords.mark(lower[start:end], func(k int) {
			if seen[k] == para {
				return
			}
			seen[k] = para
			for _, r := range d.byKeyword[k] {
				if queued[r] != para {
					queued[r] = para
					queue = append(queue, r)
				}
			}
		})
		queue = append(queue, d.always...)
		slices.Sort(queue)
		rules = rules[:0]
		for _, r := range queue {
			rules = append(rules, d.local[r])
		}
		for _, f := range d.candidates(subject[start:end], lower[start:end], rules, true) {
			f.Start += start
			f.End += start
			found.add(f)
		}
	})
	var out []Finding
	for _, f := range found {
		if f.Kind != "" {
			if !f.Manual {
				f.Value = text[f.Start:f.End]
			}
			out = append(out, f)
		}
	}
	return out
}

// paragraphs calls f with the bounds of each of text's paragraphs, which
// blank lines (\n[ \t\r]*\n) split: each starts the paragraph after it.
func paragraphs(text string, f func(start, end int)) {
	start := 0
	for i := 0; ; {
		j := strings.IndexByte(text[i:], '\n')
		if j < 0 {
			break
		}
		j += i
		i = j + 1
		for i < len(text) && (text[i] == ' ' || text[i] == '\t' || text[i] == '\r') {
			i++
		}
		if i < len(text) && text[i] == '\n' {
			f(start, j)
			start = j
			i++
		}
	}
	f(start, len(text))
}

// unfold replaces the Kelvin sign and ſ, which RE2's (?i) (like JavaScript's
// /iu) reads as k and s, by runes of the same length no rule matches. That
// drops matches with a k or s written that way, which no real key has, and
// keeps ASCII-lowercased prefixes sound.
func unfold(s string) string {
	if !strings.ContainsAny(s, "\u212aſ") {
		return s
	}
	return strings.NewReplacer("\u212a", "\u2129", "ſ", "\u017e").Replace(s)
}

// findings is sorted and without overlaps.
type findings []Finding

// add adds f unless it overlaps a finding.
func (fs *findings) add(f Finding) {
	i, _ := slices.BinarySearchFunc(*fs, f.Start, func(g Finding, start int) int {
		if g.End <= start {
			return -1
		}
		return 1
	})
	if i < len(*fs) && (*fs)[i].Start < f.End {
		return
	}
	*fs = slices.Insert(*fs, i, f)
}

// asciiLower lowercases ASCII letters only, so offsets stay put.
func asciiLower(s string) string {
	b := []byte(s)
	for i, c := range b {
		if 'A' <= c && c <= 'Z' {
			b[i] = c + 'a' - 'A'
		}
	}
	return string(b)
}

// A ruleSpec is a rule before compilation: betterleaks'
// (generated_rules.go) or ours (rules.go).
type ruleSpec struct {
	kind, label string
	// See secretGroup.
	regex string
	// Lowercase; the rule runs only on text containing one.
	keywords []string
	// The capture group holding the secret; 0 means the first that matched
	// (and isn't empty), or the whole match.
	secretGroup int
	// The secret's minimum entropy, in bits per character.
	entropy float64
	// Drop a secret made mostly of words (see wordy).
	wordy bool
	// Regexes the secret must each match.
	require []string
	allow   []allowSpec
	ok      func(secret string) bool
	// before and after report whether a match starting at start, and the
	// match m (submatch offsets in text), stand in their surroundings: the
	// lookbehinds and lookaheads RE2 lacks. A match they reject is searched
	// for again one character later.
	before func(text string, start int) bool
	after  func(text string, m []int) bool
	// extend is where in text a match ending at end really ends, or -1 if
	// it doesn't: for what RE2 can't say as briefly.
	extend func(text string, end int) int
}

type allowSpec struct {
	// The regexes test the whole match, not the secret.
	match     bool
	regexes   []string
	stopwords []string
}

type rule struct {
	kind        string
	re          *regexp.Regexp
	keywords    []int
	secretGroup int
	entropy     float64
	wordy       bool
	require     []*regexp.Regexp
	allow       []allow
	ok          func(string) bool
	before      func(string, int) bool
	after       func(string, []int) bool
	extend      func(string, int) int
	starts      starts
	// resume is re after any rune, for searching from inside a text.
	resume func() *regexp.Regexp
}

type allow struct {
	match     bool
	regexes   []*regexp.Regexp
	stopwords []string
}

func (a allow) allows(secret, match string) bool {
	subject := secret
	if a.match {
		subject = match
	}
	for _, re := range a.regexes {
		if re.MatchString(subject) {
			return true
		}
	}
	if len(a.stopwords) == 0 {
		return false
	}
	lower := strings.ToLower(secret)
	return slices.ContainsFunc(a.stopwords, func(w string) bool { return strings.Contains(lower, w) })
}

type detector struct {
	spanning, local  []*rule
	spanningKeywords []string
	keywords         *trie
	// byKeyword is the local rules (indexes) with each keyword, and always
	// those with none.
	byKeyword   [][]int
	always      []int
	globalAllow allow
	labels      map[string]string
}

// compiled is the rules, compiled on first use: it takes a while, and
// most runs of the binary that holds it never detect anything.
var compiled = sync.OnceValue(func() *detector {
	d := &detector{
		keywords:    new(trie),
		globalAllow: compileAllow(knownGlobalAllow),
		labels:      map[string]string{},
	}
	specs := slices.Concat(providerRules, knownRules, shelleyRules, []ruleSpec{namedSecret})
	for _, s := range specs {
		r := &rule{
			kind: s.kind, re: regexp.MustCompile(s.regex), secretGroup: s.secretGroup,
			entropy: s.entropy, wordy: s.wordy, ok: s.ok, before: s.before, after: s.after, extend: s.extend,
			starts: startsOf(s.regex),
			resume: sync.OnceValue(func() *regexp.Regexp { return regexp.MustCompile(`(?s:.)(?:` + s.regex + `)`) }),
		}
		for _, k := range s.keywords {
			r.keywords = append(r.keywords, d.keywords.add(k))
		}
		for _, re := range s.require {
			r.require = append(r.require, regexp.MustCompile(re))
		}
		for _, a := range s.allow {
			r.allow = append(r.allow, compileAllow(a))
		}
		if slices.Contains(s.keywords, "-----begin") {
			d.spanning = append(d.spanning, r)
			for _, k := range s.keywords {
				if !slices.Contains(d.spanningKeywords, k) {
					d.spanningKeywords = append(d.spanningKeywords, k)
				}
			}
		} else {
			for _, k := range r.keywords {
				for len(d.byKeyword) <= k {
					d.byKeyword = append(d.byKeyword, nil)
				}
				d.byKeyword[k] = append(d.byKeyword[k], len(d.local))
			}
			if len(r.keywords) == 0 {
				d.always = append(d.always, len(d.local))
			}
			d.local = append(d.local, r)
		}
		d.labels[s.kind] = s.label
	}
	d.byKeyword = append(d.byKeyword, make([][]int, len(d.keywords.words)-len(d.byKeyword))...)
	d.labels["token"] = "Random-looking token"
	d.labels["secret"] = "Marked secret"
	return d
})

func compileAllow(a allowSpec) allow {
	c := allow{match: a.match, stopwords: a.stopwords}
	for _, re := range a.regexes {
		c.regexes = append(c.regexes, regexp.MustCompile(re))
	}
	return c
}

// candidates finds the secrets rules find in text, in rule order, then
// random tokens. If lower isn't empty, it's text lowercased, for finding
// where matches can start.
func (d *detector) candidates(text, lower string, rules []*rule, tokens bool) []Finding {
	var out []Finding
	for _, r := range rules {
		r.matches(text, lower, func(m []int) {
			start, end := secretSpan(m, r.secretGroup)
			if start < 0 || start == end {
				return
			}
			value := text[start:end]
			if r.entropy > 0 && entropy(value) < r.entropy || r.ok != nil && !r.ok(value) || r.wordy && wordy(value) {
				return
			}
			for _, re := range r.require {
				if !re.MatchString(value) {
					return
				}
			}
			match := text[m[0]:m[1]]
			if d.globalAllow.allows(value, match) || slices.ContainsFunc(r.allow, func(a allow) bool { return a.allows(value, match) }) {
				return
			}
			out = append(out, Finding{Start: start, End: end, Value: value, Kind: r.kind})
		})
	}
	if tokens {
		out = append(out, randomTokens(text)...)
	}
	return out
}

// matches calls f with the submatch offsets of each match of r in text,
// which lower, if it's not empty, is lowercased.
func (r *rule) matches(text, lower string, f func(m []int)) {
	if lower == "" || r.starts.prefixes == nil {
		r.searchAll(text, nil, f)
		return
	}
	at := r.starts.find(lower)
	// Tries at each prefix can read the same text over and over (curl at
	// each curl in a long line of them): past a budget, search, which
	// takes linear time.
	budget := len(text) + 4096
	next := 0
	for i, start := range at {
		if start < next || r.before != nil && !r.before(text, start) {
			continue
		}
		if budget < 0 {
			r.searchAll(text, at[i:], f)
			return
		}
		m, work := r.starts.match(text, start)
		budget -= work
		next = start + 1
		if m = r.accept(text, m); m != nil {
			f(m)
			next = m[1]
		}
	}
}

// accept is m extended if r takes it (after its start passed r.before),
// or nil.
func (r *rule) accept(text string, m []int) []int {
	if m == nil || r.after != nil && !r.after(text, m) {
		return nil
	}
	if r.extend != nil {
		end := r.extend(text, m[1])
		if end < 0 {
			return nil
		}
		m[1] = end
	}
	return m
}

// searchAll calls f with each match of r in text that starts at one of at
// (in order), or anywhere if at is nil.
func (r *rule) searchAll(text string, at []int, f func(m []int)) {
	if at == nil && r.before == nil && r.after == nil && r.extend == nil {
		for _, m := range r.re.FindAllStringSubmatchIndex(text, -1) {
			f(m)
		}
		return
	}
	for next := 0; next <= len(text); {
		if at != nil {
			// Skip to a start that could be one.
			i, _ := slices.BinarySearch(at, next)
			for i < len(at) && r.before != nil && !r.before(text, at[i]) {
				i++
			}
			if i == len(at) {
				return
			}
			at, next = at[i:], at[i]
		}
		m := r.search(text, next)
		if m == nil {
			return
		}
		// Search on from inside a rejected match: the lookarounds this
		// replaces don't consume text.
		_, n := utf8.DecodeRuneInString(text[m[0]:])
		next = m[0] + max(n, 1)
		if r.before == nil || r.before(text, m[0]) {
			if m = r.accept(text, m); m != nil {
				f(m)
				next = max(next, m[1])
			}
		}
	}
}

// search is the submatch offsets of r's leftmost match in text that starts
// at from or after, or nil.
func (r *rule) search(text string, from int) []int {
	if from == 0 {
		return r.re.FindStringSubmatchIndex(text)
	}
	// Match the rune before from too, so assertions see it.
	_, n := utf8.DecodeLastRuneInString(text[:from])
	before := from - n
	m := r.resume().FindStringSubmatchIndex(text[before:])
	if m == nil {
		return nil
	}
	for i := range m {
		if m[i] >= 0 {
			m[i] += before
		}
	}
	_, n = utf8.DecodeRuneInString(text[m[0]:])
	m[0] += n
	return m
}

// secretSpan is where in a match its secret is, or -1.
func secretSpan(m []int, group int) (int, int) {
	if group > 0 {
		if 2*group+1 >= len(m) {
			return -1, -1
		}
		return m[2*group], m[2*group+1]
	}
	for i := 2; i+1 < len(m); i += 2 {
		if m[i] < m[i+1] {
			return m[i], m[i+1]
		}
	}
	return m[0], m[1]
}

// entropy is s's Shannon entropy, in bits per character.
func entropy(s string) float64 {
	counts := map[rune]int{}
	n := 0
	for _, c := range s {
		counts[c]++
		n++
	}
	h := 0.0
	for _, c := range counts {
		p := float64(c) / float64(n)
		h -= p * math.Log2(p)
	}
	return h
}

var wordRun = regexp.MustCompile(`[A-Z]?[a-z]{3,}|[A-Z]{4,}`)

// wordy reports whether s is mostly words (betterleaks' tokenRatio, which
// counts BPE tokens): more than 60% of it runs of 4 or more letters
// (camelCase split) that are at least 35% vowels. Random letters are about
// 20% vowels; this takes 0.2% of random lowercase-and-digit keys.
func wordy(s string) bool {
	words := 0
	for _, run := range wordRun.FindAllString(s, -1) {
		vowels := 0
		for _, c := range strings.ToLower(run) {
			if strings.ContainsRune("aeiou", c) {
				vowels++
			}
		}
		if float64(vowels) >= float64(len(run))*0.35 {
			words += len(run)
		}
	}
	return float64(words) > float64(utf8.RuneCountInString(s))*0.6
}

// notName reports whether s is a key of unknown alphabet, not a name like
// agents-typescript-sdk or YOUR_API_KEY: at most a third of it word-like
// parts between dashes and underscores (a random part of letters alone is
// almost always mixed case).
func notName(s string) bool {
	named := 0
	for _, p := range strings.FieldsFunc(s, func(c rune) bool { return c == '-' || c == '_' }) {
		if wordLike(p) {
			named += len(p)
		}
	}
	return float64(named) <= float64(utf8.RuneCountInString(s))/3
}

// wordLike reports whether p is a word: Word, word or WORD.
func wordLike(p string) bool {
	upper, lower := 0, 0
	for i := 0; i < len(p); i++ {
		switch c := p[i]; {
		case 'A' <= c && c <= 'Z' && lower == 0:
			upper++
		case 'a' <= c && c <= 'z' && upper <= 1:
			lower++
		default:
			return false
		}
	}
	return upper+lower > 0
}

func mixed(s string) bool {
	return strings.ContainsFunc(s, isUpper) && strings.ContainsFunc(s, isLower) && strings.ContainsFunc(s, isDigit)
}

func isUpper(c rune) bool { return 'A' <= c && c <= 'Z' }
func isLower(c rune) bool { return 'a' <= c && c <= 'z' }
func isDigit(c rune) bool { return '0' <= c && c <= '9' }
