package test

import (
	"fmt"
	"path/filepath"
	"strings"
	"testing"
)

func mobileCommitMessageRepo(s *shelley, subject, body string) {
	dir := mobileDiffRepo(s)
	s.write(filepath.Join(dir, "note.txt"), "fixture\n")
	s.gitIn(dir, "add", ".")
	s.gitIn(dir, "commit", "-m", subject, "-m", body)
	s.gitIn(dir, "remote", "add", "origin", "https://github.com/example/mobile-preview.git")
	s.v("HASH", s.gitIn(dir, "rev-parse", "HEAD"))
	mobileDiffConversation(s, dir)
}

func TestE2EMobileCommitMessageSelectionComment(t *testing.T) {
	e2e(t, useful(0), `On a phone, selecting rendered commit text saves just its quote in a draft review without changing the composer. Use condition waits, never sleep steps. window.selectTextIn(selector, text) selects text within an element. `+fmt.Sprintf(mobileDiffOpen, "Diffs")+`The heading ".diff-commit-message-content h2" reads "Polish mobile preview". Eval window.selectTextIn('.diff-commit-message-content h2', 'mobile preview'). The button "Comment selection" becomes visible. Click it. The ".diff-viewer-comment-dialog" is visible and its ".diff-viewer-selected-text" reads "mobile preview". Fill ".diff-viewer-comment-input" with "Please clarify" and press the comma and period keys while the input is focused; its value is "Please clarify,.". Click the dialog's "Save Comment" button. The dialog disappears, and [data-testid="message-input"] still has value exactly "". Open ".diff-viewer-header-mobile .diff-draft-review-toggle" (the visible mobile header, not the hidden desktop duplicate). The open ".diff-draft-review[open]" has one article containing "Please clarify,.". Expand its "Quote" details: the pre text is exactly "mobile preview", not "Polish mobile preview". Close the draft review with "Close draft review". Select "body excerpt" in ".diff-commit-message-body" with window.selectTextIn; "Comment selection" becomes visible. Click it and verify the dialog's selected text reads "body excerpt". Fill the input with "Body feedback" and click "Save Comment". Reopen the mobile header's review button. The review has two articles; the second contains "Body feedback", and expanding its Quote shows exactly "body excerpt". Click "Send to Chat". The diff and review close. A single ".message-user" contains both "> mobile preview" and "> body excerpt" with their respective feedback, but not "> Polish mobile preview". The composer is still empty.`, func(s *shelley) {
		s.initScript(selectTextJS)
		mobileCommitMessageRepo(s, "Polish mobile preview", "A body excerpt for review.")
	})
}

func TestE2EMobileCommitMessageExpandedLinks(t *testing.T) {
	e2e(t, useful(0), `A long mobile commit body hides its links until expanded. `+fmt.Sprintf(mobileDiffOpen, "Diffs")+`The ".diff-commit-message" has a visible "Show more" button and no ".diff-commit-message-links" element. Click "Show more". The body contains "FINAL BODY MARKER", "Show less" is visible, and ".diff-commit-message-links" has a link "Open on GitHub" with href "https://github.com/example/mobile-preview/commit/{{HASH}}" and a button "Open raw commit". Verify with eval expecting true that the body precedes the links in document order. Click "Show less"; the links are absent again. Click "Show more", then "Open raw commit". The button "View rendered" is visible and the rendered ".diff-commit-message" is absent. Click "View rendered"; the heading "Long mobile message" is visible again.`, func(s *shelley) {
		mobileCommitMessageRepo(s, "Long mobile message", strings.Repeat("Long body text for the mobile preview.\n\n", 30)+"FINAL BODY MARKER")
	})
}

func TestE2EMobileCommitMessageShortBody(t *testing.T) {
	e2e(t, useful(0), `A short mobile commit message shows its links after the body without an expand control, and its metadata Comment action saves the whole message in a draft review. Use condition waits, never sleep steps. `+fmt.Sprintf(mobileDiffOpen, "Diffs")+`The ".diff-commit-message-body" contains "Short body note". There is no "Show more" button in ".diff-commit-message". The ".diff-commit-message-links" contains "Open on GitHub" and "Open raw commit"; verify with eval expecting true that the body precedes the links in document order. Click the "Comment" button in ".diff-commit-message-mobile-meta". The comment dialog's selected text contains both "Short mobile message" and "Short body note". Fill its input with "Whole message feedback" and click "Save Comment". The comment dialog closes and [data-testid="message-input"] still has value exactly "". Click ".diff-viewer-header-mobile .diff-draft-review-toggle" (the visible mobile header, not the hidden desktop duplicate). The open ".diff-draft-review[open]" has exactly one article containing "Whole message feedback". Expand its Quote details: the pre contains "Short mobile message" and "Short body note". Click "Send to Chat". The review and diff close, and one ".message-user" contains "> Short mobile message", "> Short body note", and "Whole message feedback". The composer is still empty.`, func(s *shelley) {
		mobileCommitMessageRepo(s, "Short mobile message", "Short body note")
	})
}
