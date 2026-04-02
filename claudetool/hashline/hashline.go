// Package hashline provides line-addressable file editing using hash-anchored references.
//
// Each line is identified by its 1-indexed line number and a short hash derived from
// the normalized line text (CRC32, truncated to 2 chars from a custom alphabet).
// The combined LINE#HASH reference acts as both an address and a staleness check.
package hashline

import (
	"fmt"
	"hash/crc32"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"unicode"
)

const alphabet = "0123456789ABCDEFGHIJKLMNOPQRSTUVWXYZabcdefghijklmnopqrstuvwxyz"
const alphabetLen = 62 // len(alphabet)
const hashSpace = alphabetLen * alphabetLen * alphabetLen // 238328

// Anchor is a parsed line reference: line number + expected hash.
type Anchor struct {
	Line int
	Hash string
}

func (a Anchor) String() string {
	return fmt.Sprintf("%d#%s", a.Line, a.Hash)
}

// HashMismatch records a single line where the expected hash didn't match.
type HashMismatch struct {
	Line     int
	Expected string
	Actual   string
}

// HashMismatchError is returned when one or more hashline references have stale hashes.
type HashMismatchError struct {
	Mismatches []HashMismatch
	FileLines  []string
}

const mismatchContext = 2

func (e *HashMismatchError) Error() string {
	mismatchSet := make(map[int]HashMismatch)
	for _, m := range e.Mismatches {
		mismatchSet[m.Line] = m
	}

	displayLines := make(map[int]bool)
	for _, m := range e.Mismatches {
		lo := max(1, m.Line-mismatchContext)
		hi := min(len(e.FileLines), m.Line+mismatchContext)
		for i := lo; i <= hi; i++ {
			displayLines[i] = true
		}
	}

	sorted := make([]int, 0, len(displayLines))
	for l := range displayLines {
		sorted = append(sorted, l)
	}
	slices.Sort(sorted)

	var sb strings.Builder
	count := len(e.Mismatches)
	if count == 1 {
		sb.WriteString("1 line has changed since last read.")
	} else {
		fmt.Fprintf(&sb, "%d lines have changed since last read.", count)
	}
	sb.WriteString(" Use the updated LINE#ID references shown below (>>> marks changed lines).\n\n")

	prevLine := -1
	for _, lineNum := range sorted {
		if prevLine != -1 && lineNum > prevLine+1 {
			sb.WriteString("    ...\n")
		}
		prevLine = lineNum

		text := e.FileLines[lineNum-1]
		hash := ComputeLineHash(lineNum, text)
		tag := fmt.Sprintf("%d#%s", lineNum, hash)

		if _, ok := mismatchSet[lineNum]; ok {
			fmt.Fprintf(&sb, ">>> %s:%s\n", tag, text)
		} else {
			fmt.Fprintf(&sb, "    %s:%s\n", tag, text)
		}
	}

	return sb.String()
}

// Remaps returns a map from old tag strings to correct tag strings.
func (e *HashMismatchError) Remaps() map[string]string {
	result := make(map[string]string, len(e.Mismatches))
	for _, m := range e.Mismatches {
		result[fmt.Sprintf("%d#%s", m.Line, m.Expected)] = fmt.Sprintf("%d#%s", m.Line, m.Actual)
	}
	return result
}

func isSignificant(line string) bool {
	for _, r := range line {
		if unicode.IsLetter(r) || unicode.IsDigit(r) {
			return true
		}
	}
	return false
}

// ComputeLineHash returns a 2-character hash for a line.
// For lines with no alphanumeric content, the line number is mixed in as seed
// to reduce collisions among }, {, blank lines, etc.
func ComputeLineHash(lineNum int, line string) string {
	normalized := strings.TrimRight(strings.ReplaceAll(line, "\r", ""), " \t\n")

	var crc uint32
	if isSignificant(normalized) {
		crc = crc32.ChecksumIEEE([]byte(normalized))
	} else {
		crc = crc32.Update(uint32(lineNum), crc32.IEEETable, []byte(normalized))
	}
	v := crc % uint32(hashSpace)
	a := v / uint32(alphabetLen*alphabetLen)
	b := (v / uint32(alphabetLen)) % uint32(alphabetLen)
	c := v % uint32(alphabetLen)
	return string([]byte{alphabet[a], alphabet[b], alphabet[c]})
}

// FormatLineTag returns "42#VP" for the given line number and text.
func FormatLineTag(lineNum int, text string) string {
	return fmt.Sprintf("%d#%s", lineNum, ComputeLineHash(lineNum, text))
}

// FormatHashLines annotates every line with LINE#HASH: prefix.
func FormatHashLines(text string, startLine int) string {
	lines := strings.Split(text, "\n")
	var sb strings.Builder
	for i, line := range lines {
		num := startLine + i
		if i > 0 {
			sb.WriteByte('\n')
		}
		fmt.Fprintf(&sb, "%d#%s:%s", num, ComputeLineHash(num, line), line)
	}
	return sb.String()
}

var tagRegex = regexp.MustCompile(`^\s*[>+-]*\s*(\d+)\s*#\s*([0-9A-Za-z]{3})`)

// ParseTag parses a tag string like "42#VP" into an Anchor.
func ParseTag(ref string) (Anchor, error) {
	m := tagRegex.FindStringSubmatch(ref)
	if m == nil {
		return Anchor{}, fmt.Errorf("invalid line reference %q, expected format \"LINE#ID\" (e.g. \"5#ZZ\")", ref)
	}
	line, _ := strconv.Atoi(m[1]) // regex guarantees digits
	if line < 1 {
		return Anchor{}, fmt.Errorf("line number must be >= 1, got %d in %q", line, ref)
	}
	return Anchor{Line: line, Hash: m[2]}, nil
}

// ValidateLineRef checks that an anchor matches the current file content.
func ValidateLineRef(ref Anchor, fileLines []string) error {
	if ref.Line < 1 || ref.Line > len(fileLines) {
		return fmt.Errorf("line %d does not exist (file has %d lines)", ref.Line, len(fileLines))
	}
	actual := ComputeLineHash(ref.Line, fileLines[ref.Line-1])
	if actual != ref.Hash {
		return &HashMismatchError{
			Mismatches: []HashMismatch{{Line: ref.Line, Expected: ref.Hash, Actual: actual}},
			FileLines:  fileLines,
		}
	}
	return nil
}
