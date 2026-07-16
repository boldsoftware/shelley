package imageutil

import (
	"fmt"
	"net/http"
	"strings"
)

// Prepared contains image bytes ready to send to an LLM.
type Prepared struct {
	Data      []byte
	MediaType string
	Width     int
	Height    int
	Converted bool
	Resized   bool
}

// Prepare validates image data and fits it within a model's advertised limits.
func Prepare(data []byte, source string, maxDimension, maxBytes int) (Prepared, error) {
	converted := false
	if IsHEIC(data) {
		var err error
		data, err = ConvertHEICToPNG(data)
		if err != nil {
			return Prepared{}, fmt.Errorf("convert HEIC image %s: %w", source, err)
		}
		converted = true
	}

	mediaType := http.DetectContentType(data)
	if !strings.HasPrefix(mediaType, "image/") {
		return Prepared{}, fmt.Errorf("file is not an image: %s", mediaType)
	}
	if err := Validate(data); err != nil {
		return Prepared{}, fmt.Errorf("image file appears corrupt or truncated (%s); re-upload or pick a different file: %w", source, err)
	}

	resized := false
	format := strings.TrimPrefix(mediaType, "image/")
	if maxDimension > 0 {
		resizedData, resizedFormat, didResize, err := ResizeImage(data, maxDimension)
		if err == nil {
			data = resizedData
			format = resizedFormat
			resized = didResize
		}
	}
	if maxBytes > 0 && len(data) > maxBytes {
		return Prepared{}, fmt.Errorf(
			"image too large for model: %s is %d bytes (after any auto-resize), model limit is %d bytes; recompress the image (e.g. lower JPEG quality) and try again",
			source, len(data), maxBytes,
		)
	}

	width, height, _ := DecodeDimensions(data)
	return Prepared{
		Data:      data,
		MediaType: "image/" + format,
		Width:     width,
		Height:    height,
		Converted: converted,
		Resized:   resized,
	}, nil
}
