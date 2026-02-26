// Package shells embeds shell images for favicon
package shells

import (
	"embed"
	"encoding/base64"
	"fmt"
)

//go:embed *.png
var shellFS embed.FS

// GetShellPNGDataURI returns a data URI for a shell image based on index (0-9)
func GetShellPNGDataURI(index int) (string, error) {
	index = index % 9
	if index < 0 {
		index += 9
	}
	
	filename := fmt.Sprintf("shell_v3_%d.png", index)
	data, err := shellFS.ReadFile(filename)
	if err != nil {
		return "", err
	}
	
	return "data:image/png;base64," + base64.StdEncoding.EncodeToString(data), nil
}

// GetShellCount returns the number of available shell images
func GetShellCount() int {
	return 9
}
