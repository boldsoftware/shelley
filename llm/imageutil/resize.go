// Package imageutil provides image manipulation utilities.
package imageutil

import (
	"bytes"
	"fmt"
	"image"
	"image/jpeg"
	"image/png"
	"strings"

	"golang.org/x/image/draw"
)

// ResizeImage resizes an image if any dimension exceeds maxDimension.
// Returns the resized image bytes and the format ("png" or "jpeg").
// If no resize is needed, returns the original data unchanged.
func ResizeImage(data []byte, maxDimension int) (resized []byte, format string, didResize bool, err error) {
	img, detectedFormat, err := image.Decode(bytes.NewReader(data))
	if err != nil {
		return nil, "", false, fmt.Errorf("failed to decode image: %w", err)
	}

	bounds := img.Bounds()
	width := bounds.Dx()
	height := bounds.Dy()

	if width <= maxDimension && height <= maxDimension {
		return data, detectedFormat, false, nil
	}

	// Calculate new dimensions preserving aspect ratio
	newWidth, newHeight := width, height
	if width > height {
		newWidth = maxDimension
		newHeight = height * maxDimension / width
	} else {
		newHeight = maxDimension
		newWidth = width * maxDimension / height
	}

	// Create resized image
	resizedImg := image.NewRGBA(image.Rect(0, 0, newWidth, newHeight))
	draw.BiLinear.Scale(resizedImg, resizedImg.Bounds(), img, bounds, draw.Over, nil)

	// Encode to the same format
	var buf bytes.Buffer
	switch strings.ToLower(detectedFormat) {
	case "jpeg", "jpg":
		err = jpeg.Encode(&buf, resizedImg, &jpeg.Options{Quality: 85})
		format = "jpeg"
	default:
		err = png.Encode(&buf, resizedImg)
		format = "png"
	}

	if err != nil {
		return nil, "", false, fmt.Errorf("failed to encode resized image: %w", err)
	}

	return buf.Bytes(), format, true, nil
}

// MaxBase64Size is the Anthropic API limit for base64-encoded image data.
// Source: Claude CLI constants/apiLimits.ts (API_IMAGE_MAX_BASE64_SIZE)
const MaxBase64Size = 5 * 1024 * 1024 // 5 MB

// TargetRawSize is the maximum raw image size that stays under the base64 limit.
// base64 encoding increases size by 4/3, so: raw = base64_limit * 3/4
// Source: Claude CLI constants/apiLimits.ts (IMAGE_TARGET_RAW_SIZE)
const TargetRawSize = MaxBase64Size * 3 / 4 // 3.75 MB

// EnsureUnderMaxBytes compresses an image to stay under the Anthropic API's
// 5MB base64 limit. It follows the same cascade as Claude CLI:
//  1. If already under limit, return as-is
//  2. Try JPEG at quality 80, 60, 40, 20
//  3. Resize to progressively smaller dimensions + JPEG compression
//  4. Last resort: 400x400 JPEG at quality 20
func EnsureUnderMaxBytes(data []byte) ([]byte, string, error) {
	if len(data) <= TargetRawSize {
		// Detect format from data
		_, detectedFormat, err := image.DecodeConfig(bytes.NewReader(data))
		if err != nil {
			return data, "png", nil // default to png if we can't detect
		}
		return data, detectedFormat, nil
	}

	img, _, err := image.Decode(bytes.NewReader(data))
	if err != nil {
		return nil, "", fmt.Errorf("failed to decode image for compression: %w", err)
	}

	bounds := img.Bounds()
	width := bounds.Dx()
	height := bounds.Dy()

	// Try JPEG at progressively lower quality without resizing
	for _, quality := range []int{80, 60, 40, 20} {
		var buf bytes.Buffer
		if err := jpeg.Encode(&buf, img, &jpeg.Options{Quality: quality}); err != nil {
			continue
		}
		if buf.Len() <= TargetRawSize {
			return buf.Bytes(), "jpeg", nil
		}
	}

	// Resize to progressively smaller dimensions + JPEG compression
	for _, scale := range []float64{0.75, 0.5, 0.25} {
		newWidth := int(float64(width) * scale)
		newHeight := int(float64(height) * scale)
		if newWidth < 1 {
			newWidth = 1
		}
		if newHeight < 1 {
			newHeight = 1
		}

		resized := image.NewRGBA(image.Rect(0, 0, newWidth, newHeight))
		draw.BiLinear.Scale(resized, resized.Bounds(), img, bounds, draw.Over, nil)

		var buf bytes.Buffer
		if err := jpeg.Encode(&buf, resized, &jpeg.Options{Quality: 60}); err != nil {
			continue
		}
		if buf.Len() <= TargetRawSize {
			return buf.Bytes(), "jpeg", nil
		}
	}

	// Last resort: 400x400 JPEG at quality 20
	newWidth, newHeight := 400, 400
	if width > height {
		newHeight = height * 400 / width
	} else {
		newWidth = width * 400 / height
	}
	resized := image.NewRGBA(image.Rect(0, 0, newWidth, newHeight))
	draw.BiLinear.Scale(resized, resized.Bounds(), img, bounds, draw.Over, nil)

	var buf bytes.Buffer
	if err := jpeg.Encode(&buf, resized, &jpeg.Options{Quality: 20}); err != nil {
		return nil, "", fmt.Errorf("failed to compress image as last resort: %w", err)
	}
	return buf.Bytes(), "jpeg", nil
}
