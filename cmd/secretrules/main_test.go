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
filter = '''
(matchesAny(finding["secret"], [
  ` + "`^true$`, `AIza" + "Sy0123456789abcdefghijklmnopqrstuvw`" + `
]) || containsAny(finding["secret"], ["abcdefghijklmnopqrstuvwxyz"]))
'''
[[rules]]
id = "acme-token.1"
regex = '''\b(acme_[a-z0-9]{8})\b'''
keywords = ["ACME_"]
filter = '''
entropy(finding["secret"]) <= 2.5
|| filter.tokenRatio(finding["secret"]) >= 2.5
|| !matchesAny(finding["secret"], ["[0-9]"])
|| matchesAny(finding["match"], [` + "`EXAMPLE`" + `])
|| matchesAny(attributes["path"], [` + "`README`" + `])
'''
[[rules]]
id = "generic-api-key"
regex = '''x'''
[[rules]]
id = "acme-id"
regex = '''y'''
skipReport = true
[[rules]]
id = "acme-file"
regex = '''y'''
path = '''\.acme$'''
`))
	if err != nil {
		t.Fatal(err)
	}
	s := string(src)
	for _, want := range []string{
		`"id": "acme-token"`, `"label": "Acme token"`, `"keywords": [` + "\n      \"acme_\"", `"entropy": 2.5`,
		`"wordy": true`, `"require": [` + "\n      \"[0-9]\"", `"match": true`, `"EXAMPLE"`,
		`"^true$"`, `"stopwords": [` + "\n    \"abcdefghijklmnopqrstuvwxyz\"", "MIT License",
	} {
		if !strings.Contains(s, want) {
			t.Errorf("missing %s in\n%s", want, s)
		}
	}
	if strings.Contains(s, "README") {
		t.Errorf("path filter generated:\n%s", s)
	}
	// Key-shaped strings are escaped (see keyLike).
	if !strings.Contains(s, `"\u0041IzaSy0123456789`) {
		t.Errorf("key-shaped stopword not escaped in\n%s", s)
	}
	for _, id := range []string{"generic-api-key", "acme-id", "acme-file"} {
		if strings.Contains(s, id) {
			t.Errorf("skipped rule %s generated:\n%s", id, s)
		}
	}
}

func TestUnsupportedFilter(t *testing.T) {
	_, err := generate([]byte(`
[[rules]]
id = "acme-token"
regex = '''acme_[a-z]{8}'''
filter = '''len(finding["secret"]) > 3'''
`))
	if err == nil || !strings.Contains(err.Error(), "unsupported filter term") {
		t.Errorf("err = %v, want unsupported filter term", err)
	}
}
