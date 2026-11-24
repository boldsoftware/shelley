package secretdetect

import (
	"math/rand/v2"
	"regexp/syntax"
	"slices"
	"strings"
	"testing"
	"unicode"
)

// sample writes a random string re might match: assertions are ignored.
func sample(b *strings.Builder, re *syntax.Regexp, r *rand.Rand) {
	repeat := func(lo, hi int) {
		if hi < 0 {
			hi = lo + 3
		}
		for range lo + r.IntN(hi-lo+1) {
			sample(b, re.Sub[0], r)
		}
	}
	switch re.Op {
	case syntax.OpLiteral:
		for _, c := range re.Rune {
			if re.Flags&syntax.FoldCase != 0 && r.IntN(2) == 0 {
				c = unicode.SimpleFold(c)
			}
			b.WriteRune(c)
		}
	case syntax.OpCharClass:
		var ascii []rune
		for i := 0; i < len(re.Rune); i += 2 {
			for c := max(re.Rune[i], '\t'); c <= min(re.Rune[i+1], '~'); c++ {
				ascii = append(ascii, c)
			}
		}
		if len(ascii) > 0 && r.IntN(20) > 0 {
			b.WriteRune(ascii[r.IntN(len(ascii))])
			break
		}
		i := 2 * r.IntN(len(re.Rune)/2)
		lo, hi := re.Rune[i], min(re.Rune[i+1], 0x2fff)
		b.WriteRune(lo + r.Int32N(max(hi-lo, 0)+1))
	case syntax.OpAnyChar, syntax.OpAnyCharNotNL:
		b.WriteByte(byte(' ' + r.IntN(95)))
	case syntax.OpCapture:
		sample(b, re.Sub[0], r)
	case syntax.OpStar:
		repeat(0, 3)
	case syntax.OpPlus:
		repeat(1, 4)
	case syntax.OpQuest:
		repeat(0, 1)
	case syntax.OpRepeat:
		repeat(re.Min, min(re.Max, re.Min+3))
	case syntax.OpConcat:
		for _, sub := range re.Sub {
			sample(b, sub, r)
		}
	case syntax.OpAlternate:
		sample(b, re.Sub[r.IntN(len(re.Sub))], r)
	}
}

// Skipping to prefixes finds what searching the whole text does.
func TestStarts(t *testing.T) {
	d := compiled()
	r := rand.New(rand.NewPCG(1, 2))
	contexts := []string{"%s", "x%s", "_%s_", "a %s.", "key = \"%s\"\n", "%s%s", "é%sK", "\x82%s"}
	found, skipped := 0, 0
	for _, rl := range slices.Concat(d.spanning, d.local) {
		if rl.starts.prefixes == nil {
			skipped++
			continue
		}
		re, err := syntax.Parse(rl.re.String(), syntax.Perl)
		if err != nil {
			t.Fatal(err)
		}
		for range 6 {
			var b strings.Builder
			sample(&b, re, r)
			s := b.String()
			for _, c := range contexts {
				text := strings.ReplaceAll(c, "%s", s)
				var got, want [][]int
				rl.matches(text, asciiLower(text), func(m []int) { got = append(got, m) })
				rl.matches(text, "", func(m []int) { want = append(want, m) })
				if !slices.EqualFunc(got, want, slices.Equal) {
					t.Fatalf("%s in %q: got %v, want %v", rl.kind, text, got, want)
				}
				found += len(want)
			}
		}
	}
	t.Logf("found %d, skipped %d", found, skipped)
	// The samples match, mostly.
	if found < 5000 {
		t.Errorf("found %d matches", found)
	}
	// A few rules start with something like (?:^|[^a-z]).
	if skipped > 10 {
		t.Errorf("%d rules have no prefixes", skipped)
	}
}

func TestPrefixes(t *testing.T) {
	for _, tt := range []struct {
		re   string
		want []string
	}{
		{`ghp_[0-9a-zA-Z]{36}`, []string{"ghp_"}},
		{`(?i)\b(?:kimi|moonshot)[ \t]{0,20}`, []string{"kimi", "moonshot"}},
		{`(?i)(?:api[_-]?key|secret)`, []string{"api", "secret"}},
		{`xox[bp]-`, []string{"xox"}},
		{`x[bp]-`, []string{"xb-", "xp-"}},
		{`[0-9]{8,10}:AA`, []string{"0", "1", "2", "3", "4", "5", "6", "7", "8", "9"}},
		{`(?:azure[_.]*)?key`, []string{"azure", "key"}},
		{`a(?:bc)?d`, []string{"abcd", "ad"}},
		{`ab+c`, []string{"ab"}},
		{`(?:^|[^a-z])key`, nil},
		{`[a-z]{3}\.foo`, nil},
		{`x?`, nil},
		{`.foo`, nil},
		{`éa`, nil},
		{`aé`, []string{"a"}},
		{`(?i)sk`, []string{"sk"}},
	} {
		re, err := syntax.Parse(tt.re, syntax.Perl)
		if err != nil {
			t.Fatal(err)
		}
		if got := prefixes(re); !slices.Equal(got, tt.want) {
			t.Errorf("prefixes(%#q) = %q, want %q", tt.re, got, tt.want)
		}
	}
}

func TestMaxLen(t *testing.T) {
	for re, want := range map[string]int{
		`abc`:          3,
		`a{2,5}b?`:     6,
		`(?:ab|c)\b$`:  2,
		`a+`:           -1,
		`[a-z]{0,20}x`: 21,
		`(?:)*`:        0,
	} {
		parsed, err := syntax.Parse(re, syntax.Perl)
		if err != nil {
			t.Fatal(err)
		}
		if got := maxLen(parsed); got != want {
			t.Errorf("maxLen(%#q) = %d, want %d", re, got, want)
		}
	}
}
