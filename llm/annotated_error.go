package llm

import (
	"errors"
	"strings"
)

// AnnotatedError puts a user-facing note above a request error without
// changing what the error is. Unwrap returns the original error, and
// ErrorText lets text-based classifiers ignore the note.
type AnnotatedError struct {
	Note string
	Err  error
}

func (e *AnnotatedError) Error() string { return e.Note + "\n" + e.Err.Error() }

func (e *AnnotatedError) Unwrap() error { return e.Err }

// ErrorText returns err.Error() with any AnnotatedError note removed, so
// classifiers that match provider error text see only the provider's words.
// Text added by wrappers outside the annotation is kept.
func ErrorText(err error) string {
	text := err.Error()
	var annotated *AnnotatedError
	if !errors.As(err, &annotated) {
		return text
	}
	return strings.Replace(text, annotated.Error(), annotated.Err.Error(), 1)
}
