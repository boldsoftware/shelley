package server

import (
	"testing"

	"shelley.exe.dev/featureflags"
)

func TestFlagToolPillsRegistered(t *testing.T) {
	f, ok := featureflags.Lookup("tool-pills")
	if !ok {
		t.Fatal("tool-pills not registered")
	}
	if f.Default != false {
		t.Fatalf("default = %v, want false", f.Default)
	}
}

func TestFlagReflectionEmojiFaviconRegistered(t *testing.T) {
	f, ok := featureflags.Lookup("reflection-emoji-favicon")
	if !ok {
		t.Fatal("reflection-emoji-favicon not registered")
	}
	if f.Default != true {
		t.Fatalf("default = %v, want true", f.Default)
	}
}

func TestPatchStrategyFlagsRegisteredOff(t *testing.T) {
	for _, name := range []string{"patch-simple", "patch-openai-raw"} {
		flag, ok := featureflags.Lookup(name)
		if !ok {
			t.Fatalf("%s not registered", name)
		}
		if flag.Default != false {
			t.Fatalf("%s default = %v, want false", name, flag.Default)
		}
	}
}
