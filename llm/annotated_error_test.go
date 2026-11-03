package llm

import (
	"errors"
	"fmt"
	"testing"
)

func TestAnnotatedError(t *testing.T) {
	upstream := errors.New("status 400: credit balance is too low")
	annotated := &AnnotatedError{Note: `custom model "EOF" used its own API key`, Err: upstream}

	if got, want := annotated.Error(), `custom model "EOF" used its own API key`+"\n"+upstream.Error(); got != want {
		t.Fatalf("Error() = %q, want %q", got, want)
	}
	outer := fmt.Errorf("request exceeded 1m0s: %w", annotated)
	for _, tc := range []struct {
		name string
		err  error
		want string
	}{
		{"annotated", annotated, upstream.Error()},
		{"inside outer wrapper", outer, "request exceeded 1m0s: " + upstream.Error()},
		{"no annotation", upstream, upstream.Error()},
	} {
		if got := ErrorText(tc.err); got != tc.want {
			t.Errorf("%s: ErrorText = %q, want %q", tc.name, got, tc.want)
		}
	}

	if !errors.Is(outer, upstream) {
		t.Error("errors.Is lost the upstream error")
	}
	var found *AnnotatedError
	if !errors.As(outer, &found) || found != annotated {
		t.Error("errors.As did not find the annotation")
	}
	stall := &stallError{}
	if info, ok := RequestErrorInfoFromError(&AnnotatedError{Note: "note", Err: stall}); !ok || info.IdleStallDuration != 1 {
		t.Errorf("RequestErrorInfoFromError through annotation = %+v, %v", info, ok)
	}
}

type stallError struct{}

func (*stallError) Error() string { return "stalled" }

func (*stallError) RequestErrorInfo() RequestErrorInfo {
	return RequestErrorInfo{IdleStallDuration: 1}
}
