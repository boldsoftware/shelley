package secretdetect

import (
	"io"
	"regexp"
	"regexp/syntax"
	"slices"
	"strings"
	"unicode/utf8"
)

// Go's regexp skips ahead only to a case-sensitive literal prefix: a rule
// like (?i)(?:kimi|moonshot)… costs every position of the text a step of
// its machine. A rule's prefixes say where its matches can start, so it can
// try just those positions, with its regex anchored there.

const (
	maxPrefixes   = 16
	maxClassChars = 16
	// Prefixes this long are rare enough: extending them by alternatives
	// would only multiply the searches for them. So would extending shorter
	// ones by many alternatives.
	enoughPrefix = 3
)

// prefixes is a set of lowercase ASCII strings one of which starts every
// match of re in ASCII-lowercased text (except for Unicode's K and ſ, which
// (?i) matches as k and s), or nil if there's no such set.
func prefixes(re *syntax.Regexp) []string {
	var out []string
	for _, p := range prefixSet(re) {
		if p.s == "" {
			return nil
		}
		out = append(out, p.s)
	}
	// Drop what another prefix starts: sorted, it comes right after.
	slices.Sort(out)
	var short []string
	for _, p := range out {
		if len(short) == 0 || !strings.HasPrefix(p, short[len(short)-1]) {
			short = append(short, p)
		}
	}
	return short
}

// A prefix starts a match, and is exact if it can be the whole match, so
// that what follows the match can extend it.
type prefix struct {
	s     string
	exact bool
}

// prefixSet is prefixes one of which starts every match of re, or nil if
// there's no such set.
func prefixSet(re *syntax.Regexp) []prefix {
	empty := []prefix{{"", true}}
	switch re.Op {
	case syntax.OpEmptyMatch, syntax.OpBeginLine, syntax.OpEndLine, syntax.OpBeginText,
		syntax.OpEndText, syntax.OpWordBoundary, syntax.OpNoWordBoundary:
		return empty
	case syntax.OpLiteral:
		var b []byte
		for _, r := range re.Rune {
			if r >= utf8.RuneSelf {
				if len(b) == 0 {
					return nil
				}
				return []prefix{{string(b), false}}
			}
			b = append(b, byte(lowerASCII(r)))
		}
		return []prefix{{string(b), true}}
	case syntax.OpCharClass:
		var set []prefix
		for i := 0; i < len(re.Rune); i += 2 {
			lo, hi := re.Rune[i], re.Rune[i+1]
			if hi >= utf8.RuneSelf {
				if lo != hi || lo != 'ſ' && lo != '\u212a' {
					return nil
				}
				continue
			}
			for r := lo; r <= hi; r++ {
				set = union(set, prefix{string(lowerASCII(r)), true})
			}
		}
		return limit(set, maxClassChars)
	case syntax.OpCapture:
		return prefixSet(re.Sub[0])
	case syntax.OpQuest, syntax.OpStar, syntax.OpPlus, syntax.OpRepeat:
		set := prefixSet(re.Sub[0])
		if set == nil {
			return nil
		}
		if re.Op != syntax.OpQuest && (re.Op != syntax.OpRepeat || re.Max != 1) {
			set = inexact(set)
		}
		if re.Op == syntax.OpQuest || re.Op == syntax.OpStar || re.Op == syntax.OpRepeat && re.Min == 0 {
			set = union(set, empty...)
		}
		return limit(set, maxPrefixes)
	case syntax.OpAlternate:
		var set []prefix
		for _, sub := range re.Sub {
			s := prefixSet(sub)
			if s == nil {
				return nil
			}
			set = union(set, s...)
		}
		return limit(set, maxPrefixes)
	case syntax.OpConcat:
		set := empty
		for _, sub := range re.Sub {
			next := prefixSet(sub)
			if next == nil {
				return inexact(set)
			}
			var cat []prefix
			for _, a := range set {
				switch {
				case !a.exact:
					cat = union(cat, a)
				case len(next) > 1 && (len(a.s) >= enoughPrefix || a.s != "" && len(next) > 4):
					cat = union(cat, prefix{a.s, false})
				default:
					for _, b := range next {
						cat = union(cat, prefix{a.s + b.s, b.exact})
					}
				}
			}
			if len(cat) > maxPrefixes {
				return inexact(set)
			}
			set = cat
		}
		return set
	}
	return nil
}

// union adds to set the prefixes it lacks.
func union(set []prefix, add ...prefix) []prefix {
	for _, p := range add {
		if !slices.Contains(set, p) {
			set = append(set, p)
		}
	}
	return set
}

// limit is set, or nil if it holds more than n prefixes.
func limit(set []prefix, n int) []prefix {
	if len(set) > n {
		return nil
	}
	return set
}

func inexact(set []prefix) []prefix {
	var out []prefix
	for _, p := range set {
		out = union(out, prefix{p.s, false})
	}
	return out
}

func lowerASCII(r rune) rune {
	if 'A' <= r && r <= 'Z' {
		return r + 'a' - 'A'
	}
	return r
}

// maxLen is the most characters a match of re can have, or -1 for no limit.
func maxLen(re *syntax.Regexp) int {
	switch re.Op {
	case syntax.OpLiteral:
		return len(re.Rune)
	case syntax.OpCharClass, syntax.OpAnyChar, syntax.OpAnyCharNotNL:
		return 1
	case syntax.OpCapture, syntax.OpQuest:
		return maxLen(re.Sub[0])
	case syntax.OpStar, syntax.OpPlus:
		if maxLen(re.Sub[0]) == 0 {
			return 0
		}
		return -1
	case syntax.OpRepeat:
		n := maxLen(re.Sub[0])
		if n == 0 {
			return 0
		}
		if n < 0 || re.Max < 0 {
			return -1
		}
		return n * re.Max
	case syntax.OpConcat, syntax.OpAlternate:
		total := 0
		for _, sub := range re.Sub {
			n := maxLen(sub)
			if n < 0 {
				return -1
			}
			if re.Op == syntax.OpConcat {
				total += n
			} else {
				total = max(total, n)
			}
		}
		return total
	}
	return 0
}

// starts finds where a regex's matches can start, and matches it there.
type starts struct {
	prefixes []string
	// first matches the regex at the start of a text, and after matches
	// it after the text's first rune, so assertions see the rune before.
	first, after *regexp.Regexp
	// runes is the most runes a match can take, and span the most bytes it
	// and the rune after it can, or -1 for no limit.
	runes, span int
}

func startsOf(expr string) starts {
	re, err := syntax.Parse(expr, syntax.Perl)
	if err != nil {
		panic(err)
	}
	s := starts{prefixes: prefixes(re), runes: maxLen(re), span: -1}
	if s.prefixes == nil {
		return s
	}
	s.first = regexp.MustCompile(`^(?:` + expr + `)`)
	s.after = regexp.MustCompile(`^(?s:.)(?:` + expr + `)`)
	if s.runes >= 0 {
		s.span = (s.runes + 1) * utf8.UTFMax
	}
	return s
}

// find is where in lower a prefix starts, in order.
func (s starts) find(lower string) []int {
	var at []int
	for _, p := range s.prefixes {
		for i := 0; ; {
			j := strings.Index(lower[i:], p)
			if j < 0 {
				break
			}
			at = append(at, i+j)
			i += j + 1
		}
	}
	if len(s.prefixes) > 1 {
		slices.Sort(at)
		at = slices.Compact(at)
	}
	return at
}

// maxSpan is the longest span the backtracker gets: it clears a bit per
// instruction and byte of text on every try. The NFA's cost is just the
// runes it reads.
const maxSpan = 1024

// match is the submatch offsets of the regex's match at text[at:], or nil,
// and a measure of the work it took: bytes the NFA read, which cost more
// than the backtracker's, or the most the backtracker could read.
func (s starts) match(text string, at int) ([]int, int) {
	end := len(text)
	if s.span >= 0 {
		end = min(end, at+s.span)
	}
	re, from := s.first, at
	if at > 0 {
		_, n := utf8.DecodeLastRuneInString(text[:at])
		re, from = s.after, at-n
	}
	var m []int
	var work int
	if s.span >= 0 && s.span <= maxSpan {
		m = re.FindStringSubmatchIndex(text[from:end])
		work = min(end-from, s.runes+2)
	} else {
		in := &runeReader{s: text[from:end]}
		m = re.FindReaderSubmatchIndex(in)
		work = 4 * in.i
	}
	if m == nil {
		return nil, work
	}
	for i := range m {
		if m[i] >= 0 {
			m[i] += from
		}
	}
	m[0] = at
	return m, work
}

// runeReader reads s, and counts the bytes it read.
type runeReader struct {
	s string
	i int
}

func (r *runeReader) ReadRune() (rune, int, error) {
	if r.i >= len(r.s) {
		return 0, 0, io.EOF
	}
	c, n := utf8.DecodeRuneInString(r.s[r.i:])
	r.i += n
	return c, n, nil
}
