package secretdetect

import (
	"math"
	"strings"
)

// randomTokens finds long random tokens in text: runs that could be one,
// bounded by whitespace, quotes, brackets or punctuation that can't be
// inside one (an email's @, a hostname's dot, a URL's colon), that look
// random. A slash anywhere means a path or URL.
func randomTokens(text string) []Finding {
	var out []Finding
	for i := 0; i < len(text); {
		if !tokenChar(text[i]) {
			i++
			continue
		}
		start := i
		for i < len(text) && tokenChar(text[i]) {
			i++
		}
		if i-start < 32 || start > 0 && strings.IndexByte(".:@", text[start-1]) >= 0 || i < len(text) && text[i] == '@' {
			continue
		}
		// = inside a run assigns (NAME=TOKEN); base64 has it only at the end.
		start += strings.LastIndexByte(strings.TrimRight(text[start:i], "="), '=') + 1
		if looksRandom(text[start:i]) {
			out = append(out, Finding{Start: start, End: i, Value: text[start:i], Kind: "token"})
		}
	}
	return out
}

func tokenChar(c byte) bool {
	return 'a' <= c && c <= 'z' || 'A' <= c && c <= 'Z' || '0' <= c && c <= '9' || strings.IndexByte("_+/=-", c) >= 0
}

// looksRandom reports whether token, a run of tokenChar, is a long random
// one: 32 to 512 characters of base64/base62, with upper and lower case and
// digits, high entropy, and not mostly words (a long CamelCase
// identifier), hex (hashes), or a path or URL (no slashes). Base64url has 2
// separators in 64 characters, so a long token has a few dashes and
// underscores; a snake_case or kebab-case name has many more.
func looksRandom(token string) bool {
	n := float64(len(token))
	if n < 32 || n > 512 || !mixed(token) ||
		strings.ContainsRune(token, '/') ||
		strings.Trim(token, "0123456789abcdefABCDEF-") == "" ||
		float64(strings.Count(token, "-")+strings.Count(token, "_")) > math.Max(3, n/12) {
		return false
	}
	digits := 0
	for _, c := range token {
		if isDigit(c) {
			digits++
		}
	}
	return digits >= 3 && float64(words(token)) <= n/3 && entropy(token) >= 4.2
}

// words is how much of s is words: runs of four or more lowercase letters,
// and a capital before.
func words(s string) int {
	n := 0
	for i := 0; i < len(s); i++ {
		j := i
		for j < len(s) && isLower(rune(s[j])) {
			j++
		}
		if j-i >= 4 {
			n += j - i
			if i > 0 && isUpper(rune(s[i-1])) {
				n++
			}
		}
		i = j
	}
	return n
}
