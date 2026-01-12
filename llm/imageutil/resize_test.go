package imageutil

import (
	"bytes"
	"image"
	"image/color"
	"image/jpeg"
	"image/png"
	"testing"
)

func createTestPNG(t *testing.T, width, height int) []byte {
	img := image.NewRGBA(image.Rect(0, 0, width, height))
	for y := 0; y < height; y++ {
		for x := 0; x < width; x++ {
			img.Set(x, y, color.RGBA{R: 100, G: 150, B: 200, A: 255})
		}
	}
	var buf bytes.Buffer
	if err := png.Encode(&buf, img); err != nil {
		t.Fatalf("Failed to create test image: %v", err)
	}
	return buf.Bytes()
}

func TestResizeImage(t *testing.T) {
	tests := []struct {
		name       string
		width      int
		height     int
		maxDim     int
		wantResize bool
		wantMaxDim int
	}{
		{"small image", 800, 600, 2000, false, 800},
		{"at limit", 2000, 2000, 2000, false, 2000},
		{"width exceeds", 3000, 1000, 2000, true, 2000},
		{"height exceeds", 1000, 3000, 2000, true, 2000},
		{"both exceed", 3000, 3000, 2000, true, 2000},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			data := createTestPNG(t, tt.width, tt.height)
			resized, format, didResize, err := ResizeImage(data, tt.maxDim)
			if err != nil {
				t.Fatalf("ResizeImage() error = %v", err)
			}
			if didResize != tt.wantResize {
				t.Errorf("ResizeImage() didResize = %v, want %v", didResize, tt.wantResize)
			}
			if format != "png" {
				t.Errorf("ResizeImage() format = %v, want png", format)
			}
			if didResize {
				// Verify the resized image dimensions
				config, _, err := image.DecodeConfig(bytes.NewReader(resized))
				if err != nil {
					t.Fatalf("Failed to decode resized image: %v", err)
				}
				if config.Width > tt.maxDim || config.Height > tt.maxDim {
					t.Errorf("Resized image %dx%d still exceeds max %d", config.Width, config.Height, tt.maxDim)
				}
			} else {
				if !bytes.Equal(resized, data) {
					t.Error("Expected original data when no resize needed")
				}
			}
		})
	}
}

func TestResizeImageJPEG(t *testing.T) {
	// Create a test JPEG image
	img := image.NewRGBA(image.Rect(0, 0, 3000, 1000))
	for y := 0; y < 1000; y++ {
		for x := 0; x < 3000; x++ {
			img.Set(x, y, color.RGBA{R: 100, G: 150, B: 200, A: 255})
		}
	}
	var buf bytes.Buffer
	if err := jpeg.Encode(&buf, img, &jpeg.Options{Quality: 85}); err != nil {
		t.Fatalf("Failed to create test JPEG image: %v", err)
	}
	data := buf.Bytes()

	resized, format, didResize, err := ResizeImage(data, 2000)
	if err != nil {
		t.Fatalf("ResizeImage() error = %v", err)
	}
	if !didResize {
		t.Error("Expected resize for large JPEG image")
	}
	if format != "jpeg" {
		t.Errorf("ResizeImage() format = %v, want jpeg", format)
	}

	// Verify the resized image dimensions
	config, _, err := image.DecodeConfig(bytes.NewReader(resized))
	if err != nil {
		t.Fatalf("Failed to decode resized image: %v", err)
	}
	if config.Width > 2000 || config.Height > 2000 {
		t.Errorf("Resized image %dx%d still exceeds max 2000", config.Width, config.Height)
	}
}

func TestResizeImageErrors(t *testing.T) {
	tests := []struct {
		name    string
		data    []byte
		maxDim  int
		wantErr bool
	}{
		{
			name:    "empty data",
			data:    []byte{},
			maxDim:  2000,
			wantErr: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, _, _, err := ResizeImage(tt.data, tt.maxDim)
			if (err != nil) != tt.wantErr {
				t.Errorf("ResizeImage() error = %v, wantErr %v", err, tt.wantErr)
			}
		})
	}
}

func TestResizeImageNoResizeNeeded(t *testing.T) {
	data := createTestPNG(t, 800, 600)
	resized, format, didResize, err := ResizeImage(data, 2000)
	if err != nil {
		t.Fatalf("ResizeImage() error = %v", err)
	}
	if didResize {
		t.Error("Expected no resize for small image")
	}
	if format != "png" {
		t.Errorf("ResizeImage() format = %v, want png", format)
	}
	if !bytes.Equal(resized, data) {
		t.Error("Expected original data when no resize needed")
	}
}
