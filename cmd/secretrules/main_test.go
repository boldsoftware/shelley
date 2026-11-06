package main

import (
	"strings"
	"testing"
)

func TestToJS(t *testing.T) {
	for _, tc := range []struct{ re, js string }{
		{`(?i)ab1`, `[Aa][Bb]1`},
		{`x(?i:k)`, `x[Kk\u{212a}]`},
		{`[^a]`, `[\u{0}-` + "`" + `b-\u{10ffff}]`},
		{`(?-i:ab)|c\z`, `(?:ab|c$)`},
		{`a{2,}?b+\.`, `(?:a){2,}?(?:b)+\.`},
		{`[\w.-]{0,50}?(?:key)=([a-z]{4})`, `key=((?:[a-z]){4})`}, // leading run trimmed
		{`[\w.-]{0,50}?(?i:[\w.-]{0,50}?(?:okta))=`, `[Oo][Kk\u{212a}][Tt][Aa]=`},
		{`(?:[\w.-]{0,50}?(?:jira)=x|ATATT3[a-z])`, `(?:jira=x|ATATT3[a-z])`},
		{`a?(b)`, `(b)`},
		{`(a?)b`, `((?:a)?)b`},
		{`/[\]-]`, `\/[\-\]]`},
	} {
		got, err := toJS(tc.re, true)
		if err != nil || got != tc.js {
			t.Errorf("toJS(%q) = %q, %v; want %q", tc.re, got, err, tc.js)
		}
	}
}

func TestGenerate(t *testing.T) {
	src, err := generate([]byte(`
[allowlist]
regexes = ['''^true$''', '''AIza` + `Sy0123456789abcdefghijklmnopqrstuvw''']
[[rules]]
id = "acme-token"
regex = '''\b(acme_[a-z0-9]{8})\b'''
keywords = ["ACME_"]
entropy = 2
[[rules]]
id = "generic-api-key"
regex = '''x'''
[[rules]]
id = "acme-file"
regex = '''y'''
path = '''\.acme$'''
`))
	if err != nil {
		t.Fatal(err)
	}
	s := string(src)
	for _, want := range []string{`"id": "acme-token"`, `"label": "Acme token"`, `"keywords": [` + "\n      \"acme_\"", `"entropy": 2`, `"^true$"`, "MIT License"} {
		if !strings.Contains(s, want) {
			t.Errorf("missing %s in\n%s", want, s)
		}
	}
	// Key-shaped strings are escaped (see keyLike).
	if !strings.Contains(s, `"\u0041IzaSy0123456789`) {
		t.Errorf("key-shaped stopword not escaped in\n%s", s)
	}
	if strings.Contains(s, "generic-api-key") || strings.Contains(s, "acme-file") {
		t.Errorf("skipped rules generated:\n%s", s)
	}
}
