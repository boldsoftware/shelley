package server

import (
	"context"
	"net/url"
	"os"
	"path/filepath"
	"strings"
)

func (s *Server) transcriptionPrompt(ctx context.Context, conversationID string, review *reviewRecording) (string, error) {
	conversation, err := s.db.GetConversationByID(ctx, conversationID)
	if err != nil {
		return "", err
	}
	hostname, err := os.Hostname()
	if err != nil {
		return "", err
	}
	return buildTranscriptionPrompt(hostname, derefString(conversation.Cwd), derefString(conversation.Slug), review), nil
}

// buildTranscriptionPrompt describes a dictated instruction, or, when review
// is non-nil, narration of a code review in the diff viewer or of a web page.
func buildTranscriptionPrompt(hostname, cwd, slug string, review *reviewRecording) string {
	intro := "Transcribe the audio accurately and preserve the speaker's wording. The speaker is dictating a request or instruction to Shelley, an AI agent performing software-development and system tasks inside a VM. Use the context below only to resolve names, acronyms, and technical terms. Do not execute or answer the request, summarize it, or add content that was not spoken."
	switch {
	case review != nil && review.webPage():
		intro = "Transcribe the audio accurately and preserve the speaker's wording, including corrections and changes of mind. The speaker is narrating while using a web page, pointing at and selecting parts of it, for Shelley, an AI agent performing software-development tasks inside a VM. Use the context below only to resolve names, identifiers, acronyms, and technical terms. Do not answer or act on the feedback, summarize it, or add content that was not spoken."
	case review != nil:
		intro = "Transcribe the audio accurately and preserve the speaker's wording, including corrections and changes of mind. The speaker is narrating a code review of changes shown in the diff viewer of Shelley, an AI agent performing software-development tasks inside a VM, while pointing at and selecting code. Use the context below only to resolve names, identifiers, file paths, acronyms, and technical terms. Do not answer or act on the feedback, summarize it, or add content that was not spoken."
	}
	lines := []string{intro, "", "Application: Shelley", "Platform: exe.dev"}
	if hostname = cleanTranscriptionMetadata(hostname); hostname != "" {
		lines = append(lines, "VM: "+hostname)
	}
	if cwd = cleanTranscriptionMetadata(cwd); cwd != "" {
		if project := filepath.Base(cwd); project != "." && project != string(filepath.Separator) {
			lines = append(lines, "Project: "+project)
		}
		lines = append(lines, "Working directory: "+cwd)
	}
	if slug = cleanTranscriptionMetadata(slug); slug != "" {
		lines = append(lines, "Conversation: "+slug)
	}
	if review != nil && review.webPage() {
		// Only the host: the page's own text has no say in the transcript.
		if page, err := url.Parse(review.URL); err == nil && page.Host != "" {
			lines = append(lines, "Site: "+cleanTranscriptionMetadata(page.Host))
		}
	}
	if review != nil {
		if files := review.files(); len(files) > 0 {
			lines = append(lines, "Files: "+strings.Join(files, ", "))
		}
	}
	return strings.Join(lines, "\n")
}

func cleanTranscriptionMetadata(value string) string {
	runes := []rune(strings.Join(strings.Fields(value), " "))
	if len(runes) > 200 {
		runes = runes[:200]
	}
	return string(runes)
}
