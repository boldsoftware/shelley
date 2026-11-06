package server

import (
	"cmp"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"
)

const (
	reviewPromptFiles    = 12
	reviewPromptRunes    = 400
	reviewOnScreenItems  = 8
	reviewOnScreenDwell  = 1000
	reviewPointerDwell   = 400
	reviewClearDwell     = 500
	reviewPointerRunes   = 200
	reviewSelectionRunes = 500
	// timelinePause is the silence that starts a new phrase in a timeline.
	timelinePause = 2.0
	// timelineMaxWord bounds how long a single spoken word plausibly lasts.
	timelineMaxWord = 1.0
)

const reviewGuidance = `Treat this as review feedback to work through. Resolve "this", "here", and "that" from the pointer and selection at that moment. People think aloud, backtrack, and change their minds; later statements supersede earlier ones. Something being on screen does not mean it was read or approved. For a long review, have a subagent turn the timeline into a list of feedback items, each with its file:line referent and the user's own words, preserving wording and nuance without summarizing or drawing conclusions.`

// reviewRecording is the diff viewer's event log uploaded next to a review
// recording's audio as <audio>.review.json.
type reviewRecording struct {
	mediaPath  string
	Version    int           `json:"version"`
	Cwd        string        `json:"cwd"`
	StartedAt  string        `json:"started_at"`
	DurationMS float64       `json:"duration_ms"`
	Events     []reviewEvent `json:"events"`
}

type reviewEvent struct {
	MS      float64  `json:"ms"`
	Type    string   `json:"type"`
	Where   string   `json:"where"`
	Text    string   `json:"text"`
	Mode    string   `json:"mode"`
	Commit  string   `json:"commit"`
	File    string   `json:"file"`
	Side    string   `json:"side"`
	Line    int      `json:"line"`
	EndLine int      `json:"end_line"`
	Items   []string `json:"items"`
	Open    bool     `json:"open"`
	State   string   `json:"state"`
}

// speechTimings is the timestamps file written beside a recording: word
// timings plus sentence (Deepgram) or segment (Whisper) spans.
type speechTimings struct {
	Words    []timedWord    `json:"words"`
	Segments []timedSegment `json:"segments"`
}

type timedWord struct {
	Word  string  `json:"word"`
	Start float64 `json:"start"`
	End   float64 `json:"end"`
}

type timedSegment struct {
	Text  string  `json:"text,omitempty"`
	Start float64 `json:"start"`
	End   float64 `json:"end"`
}

// loadReviewRecording returns the review events uploaded next to mediaPath,
// or nil when mediaPath is not a diff-viewer review recording.
func loadReviewRecording(mediaPath string) (*reviewRecording, error) {
	review := &reviewRecording{mediaPath: mediaPath}
	path := review.eventsPath()
	info, err := os.Lstat(path)
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	if !info.Mode().IsRegular() {
		return nil, fmt.Errorf("%s must be a regular file", path)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	if err := json.Unmarshal(data, review); err != nil {
		return nil, fmt.Errorf("parse %s: %w", path, err)
	}
	if review.Version != 1 {
		return nil, fmt.Errorf("%s has unsupported version %d", path, review.Version)
	}
	return review, nil
}

func (r *reviewRecording) eventsPath() string   { return r.mediaPath + ".review.json" }
func (r *reviewRecording) timelinePath() string { return r.mediaPath + ".timeline.md" }

// files returns the first few distinct files the review touched, to help the
// transcription model spell paths and identifiers. The list is capped in
// count and length so it cannot crowd out the rest of the prompt.
func (r *reviewRecording) files() []string {
	var files []string
	runes := 0
	for _, event := range r.Events {
		file := cleanTranscriptionMetadata(event.File)
		if file == "" || slices.Contains(files, file) {
			continue
		}
		if runes += utf8.RuneCountInString(file) + 2; runes > reviewPromptRunes {
			break
		}
		if files = append(files, file); len(files) == reviewPromptFiles {
			break
		}
	}
	return files
}

// parentDetails returns the review-specific paragraphs of the transcription
// message delivered to the agent.
func (r *reviewRecording) parentDetails(timestampsPath string) []string {
	subject := "changes"
	if r.Cwd != "" {
		subject = r.Cwd
	}
	return []string{
		"Review recording: narrated while reviewing " + subject + " in the diff viewer.\n" +
			"Timeline (speech interleaved with what was on screen, pointed at, and selected): [" + r.timelinePath() + "]\n" +
			"Audio: [" + r.mediaPath + "] · Events: [" + r.eventsPath() + "] · Word timestamps: [" + timestampsPath + "]",
		reviewGuidance,
	}
}

// writeTimeline merges the word timings at timestampsPath with the
// review events and atomically replaces the timeline file.
func (r *reviewRecording) writeTimeline(timestampsPath string) error {
	return writeTimeline(r.timelinePath(), timestampsPath, func(speech speechTimings) string {
		return buildReviewTimeline(r, speech)
	})
}

// writeTimeline atomically replaces the file at path with build's rendering
// of the word timings at timestampsPath.
func writeTimeline(path, timestampsPath string, build func(speechTimings) string) error {
	data, err := os.ReadFile(timestampsPath)
	if err != nil {
		return fmt.Errorf("read transcription timestamps: %w", err)
	}
	var speech speechTimings
	if err := json.Unmarshal(data, &speech); err != nil {
		return fmt.Errorf("parse transcription timestamps: %w", err)
	}
	temp, err := os.CreateTemp(filepath.Dir(path), filepath.Base(path)+".tmp-*")
	if err != nil {
		return err
	}
	defer os.Remove(temp.Name())
	if _, err := temp.WriteString(build(speech)); err != nil {
		temp.Close()
		return err
	}
	if err := temp.Close(); err != nil {
		return err
	}
	return os.Rename(temp.Name(), path)
}

func buildReviewTimeline(r *reviewRecording, speech speechTimings) string {
	var b strings.Builder
	b.WriteString("# Review recording timeline\n\n")
	if r.Cwd != "" {
		b.WriteString("Repository: " + r.Cwd + "\n")
	}
	recorded := (time.Duration(r.DurationMS) * time.Millisecond).Round(time.Second).String()
	if r.StartedAt != "" {
		recorded = r.StartedAt + " · " + recorded
	}
	b.WriteString("Recorded: " + recorded + "\n")
	b.WriteString(`Times are from the start of the audio. "said" lines are speech; indented lines are what the page showed at that moment.` + "\n")
	if len(speech.Words) == 0 {
		b.WriteString("No speech was detected.\n")
	}
	b.WriteString("\n")
	for _, line := range timelineLines(reviewContextLines(r), speech) {
		b.WriteString(line + "\n")
	}
	return b.String()
}

type reviewContextLine struct {
	ms   float64
	text string
}

// timelineLines interleaves speech phrases with the context lines. Phrases
// break at segment boundaries, at pauses, and before any context line that
// happened by the next word's start.
func timelineLines(pending []reviewContextLine, speech speechTimings) []string {
	var lines, phrase []string
	var phraseMS float64
	flushPhrase := func() {
		if len(phrase) > 0 {
			lines = append(lines, formatReviewTime(phraseMS)+`  said: "`+strings.Join(phrase, " ")+`"`)
			phrase = nil
		}
	}
	emitContext := func(untilMS float64) {
		for len(pending) > 0 && pending[0].ms <= untilMS {
			lines = append(lines, formatReviewTime(pending[0].ms)+"    "+pending[0].text)
			pending = pending[1:]
		}
	}
	segment := 0
	var lastEnd float64
	for i, word := range speech.Words {
		text := strings.TrimSpace(word.Word)
		if text == "" {
			continue
		}
		startMS := math.Round(word.Start * 1000)
		if len(pending) > 0 && pending[0].ms <= startMS {
			flushPhrase()
			emitContext(startMS)
		}
		// Word and segment timings may disagree slightly, so assign a word
		// to a segment by its midpoint. A word stretched over the next one,
		// as Deepgram sometimes does at a sentence end, ends where it starts.
		end := word.End
		if i+1 < len(speech.Words) {
			end = math.Min(end, speech.Words[i+1].Start)
		}
		mid := (word.Start + math.Max(word.Start, end)) / 2
		for segment+1 < len(speech.Segments) && mid >= speech.Segments[segment+1].Start {
			segment++
			flushPhrase()
		}
		if word.Start-lastEnd >= timelinePause {
			flushPhrase()
		}
		if len(phrase) == 0 {
			phraseMS = startMS
		}
		phrase = append(phrase, text)
		// Recognizers sometimes stretch the word before a pause across it.
		lastEnd = math.Min(word.End, word.Start+timelineMaxWord)
	}
	flushPhrase()
	emitContext(math.Inf(1))
	return lines
}

// reviewContextLines selects the events worth showing next to the speech:
// dwells rather than transient pointer and scroll positions, without repeats.
func reviewContextLines(r *reviewRecording) []reviewContextLine {
	events := slices.Clone(r.Events)
	slices.SortStableFunc(events, func(a, b reviewEvent) int { return cmp.Compare(a.MS, b.MS) })
	end := r.DurationMS
	if len(events) > 0 {
		end = max(end, events[len(events)-1].MS)
	}
	// until returns when the state set by events[i] is replaced by an event
	// of one of the given types, or the end of the recording.
	until := func(i int, types ...string) float64 {
		for _, next := range events[i+1:] {
			if slices.Contains(types, next.Type) {
				return next.MS
			}
		}
		return end
	}

	var lines []reviewContextLine
	var lastView, lastPointer, lastSelection string
	var lastScreen *string
	for i, event := range events {
		var text string
		switch event.Type {
		case "view":
			if where := event.where(); where != lastView {
				lastView = where
				text = "view: " + where
			}
		case "on_screen":
			if len(event.Items) == 0 {
				break
			}
			key := strings.Join(event.Items, "\n")
			if lastScreen == nil || (key != *lastScreen && until(i, "on_screen")-event.MS >= reviewOnScreenDwell) {
				lastScreen = &key
				text = "on screen: " + formatReviewItems(event.Items)
			}
		case "pointer":
			key := event.where() + "\n" + event.Text
			if key != lastPointer && until(i, "pointer", "pointer_leave", "stop")-event.MS >= reviewPointerDwell {
				lastPointer = key
				text = "pointer: " + event.where()
				if snippet := reviewSnippet(event.Text, reviewPointerRunes); snippet != "" {
					quote := "`"
					if strings.Contains(snippet, "`") {
						quote = `"`
					}
					text += " — " + quote + snippet + quote
				}
			}
		case "selection":
			key := event.where() + "\n" + event.Text
			if strings.TrimSpace(event.Text) == "" {
				// A brief clear (reselecting) is noise; a lasting one matters,
				// since the old selection no longer applies.
				if lastSelection != "" && until(i, "selection")-event.MS >= reviewClearDwell {
					lastSelection = ""
					text = "selection cleared"
				}
			} else if key != lastSelection {
				lastSelection = key
				text = "selected: " + event.where() + ` — "` + reviewSnippet(event.Text, reviewSelectionRunes) + `"`
			}
		case "click":
			text = "clicked: " + event.where()
		case "comment":
			text = "comment: " + event.where() + ` — "` + reviewSnippet(event.Text, math.MaxInt) + `"`
		case "dialog":
			text = "dialog closed: " + event.where()
			if event.Open {
				text = "dialog opened: " + event.where()
			}
		case "page":
			text = map[string]string{
				"hidden":  "page hidden",
				"visible": "page visible",
				"blur":    "window lost focus",
				"focus":   "window focused",
			}[event.State]
		}
		if text != "" {
			lines = append(lines, reviewContextLine{ms: event.MS, text: text})
		}
	}
	return lines
}

// where returns the client's breadcrumb, or one built from the structured
// fields when the client sent none.
func (e reviewEvent) where() string {
	if where := strings.Join(strings.Fields(e.Where), " "); where != "" {
		return where
	}
	var parts []string
	for _, part := range []string{e.Mode, e.Commit, e.File, e.Side} {
		if part != "" {
			parts = append(parts, part)
		}
	}
	if e.Line > 0 {
		lines := strconv.Itoa(e.Line)
		if e.EndLine > e.Line {
			lines += "–" + strconv.Itoa(e.EndLine)
		}
		parts = append(parts, lines)
	}
	if len(parts) == 0 {
		return "(unlabeled)"
	}
	return strings.Join(parts, " ")
}

func formatReviewItems(items []string) string {
	if len(items) <= reviewOnScreenItems {
		return strings.Join(items, "; ")
	}
	return strings.Join(items[:reviewOnScreenItems], "; ") + fmt.Sprintf("; …+%d more", len(items)-reviewOnScreenItems)
}

// reviewSnippet renders text on one line, truncated to limit runes.
func reviewSnippet(text string, limit int) string {
	text = strings.TrimSpace(text)
	text = strings.NewReplacer("\r\n", "⏎", "\n", "⏎", "\r", "⏎").Replace(text)
	if runes := []rune(text); len(runes) > limit {
		return string(runes[:limit]) + "…"
	}
	return text
}

// formatReviewTime formats ms since the start of the audio as MM:SS.s, or
// H:MM:SS.s from an hour on.
func formatReviewTime(ms float64) string {
	tenths := int64(max(ms, 0) / 100)
	seconds, minutes := tenths/10%60, tenths/600
	if minutes >= 60 {
		return fmt.Sprintf("%d:%02d:%02d.%d", minutes/60, minutes%60, seconds, tenths%10)
	}
	return fmt.Sprintf("%02d:%02d.%d", minutes, seconds, tenths%10)
}
