package imageutil

import (
	"bytes"
	"image/png"
	"os"
	"testing"
)

func TestIsHEIC(t *testing.T) {
	tests := []struct {
		name string
		data []byte
		want bool
	}{
		{"empty", []byte{}, false},
		{"too short", []byte{0, 0, 0, 0, 'f', 't', 'y', 'p'}, false},
		{"heic brand", []byte{0, 0, 0, 0, 'f', 't', 'y', 'p', 'h', 'e', 'i', 'c'}, true},
		{"heix brand", []byte{0, 0, 0, 0, 'f', 't', 'y', 'p', 'h', 'e', 'i', 'x'}, true},
		{"mif1 brand", []byte{0, 0, 0, 0, 'f', 't', 'y', 'p', 'm', 'i', 'f', '1'}, true},
		{"avif brand", []byte{0, 0, 0, 0, 'f', 't', 'y', 'p', 'a', 'v', 'i', 'f'}, true},
		{"not ftyp", []byte{0, 0, 0, 0, 'x', 'x', 'x', 'x', 'h', 'e', 'i', 'c'}, false},
		{"unknown brand", []byte{0, 0, 0, 0, 'f', 't', 'y', 'p', 'i', 's', 'o', 'm'}, false},
		{"png", []byte{0x89, 'P', 'N', 'G', 0x0d, 0x0a, 0x1a, 0x0a, 0, 0, 0, 0}, false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := IsHEIC(tt.data); got != tt.want {
				t.Errorf("IsHEIC() = %v, want %v", got, tt.want)
			}
		})
	}
}

func TestConvertHEICToPNG(t *testing.T) {
	// Skip if no real HEIC test file available
	testFile := "/tmp/shelley-uploads/upload_349d2aa15d2b3e4e.heic"
	data, err := os.ReadFile(testFile)
	if err != nil {
		t.Skipf("test HEIC file not available: %v", err)
	}

	if !IsHEIC(data) {
		t.Fatal("test file should be detected as HEIC")
	}

	pngData, err := ConvertHEICToPNG(data)
	if err != nil {
		t.Fatalf("ConvertHEICToPNG failed: %v", err)
	}

	// Verify it's valid PNG
	_, err = png.Decode(bytes.NewReader(pngData))
	if err != nil {
		t.Fatalf("result is not valid PNG: %v", err)
	}
}
