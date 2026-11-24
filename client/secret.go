package client

import (
	"cmp"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"strings"
)

const secretUsage = `Usage: shelley secret [-url URL] ID

Write a secret that Shelley took out of a message to a new temporary file
(mode 0600), and print the file's path. Secrets expire a day after they
were sent.

-url defaults to unix://$SHELLEY_SOCKET, which Shelley sets for the
commands it runs.
`

// RunSecret is the entry point for "shelley secret [args...]".
func RunSecret(args []string) {
	os.Exit(runSecret(args, os.Stdout, os.Stderr))
}

func runSecret(args []string, stdout, stderr io.Writer) int {
	fs := flag.NewFlagSet("secret", flag.ContinueOnError)
	serverURL := fs.String("url", defaultClientURL(), "")
	pos, err := parse(fs, args, 1, 1)
	var ue usageError
	switch {
	case errors.Is(err, flag.ErrHelp):
		fmt.Fprint(stdout, secretUsage)
		return 0
	case errors.As(err, &ue):
		fmt.Fprintf(stderr, "shelley secret: %v\n\n%s", err, secretUsage)
		return 2
	case err == nil:
		var path string
		if path, err = readSecret(*serverURL, pos[0]); err == nil {
			fmt.Fprintln(stdout, path)
			return 0
		}
	}
	fmt.Fprintf(stderr, "shelley secret: %v\n", err)
	return 1
}

// readSecret writes secret id to a new temporary file and returns its path.
func readSecret(serverURL, id string) (path string, err error) {
	hc, base, err := (&clientConfig{serverURL: serverURL}).newHTTPClient()
	if err != nil {
		return "", err
	}
	f, err := os.CreateTemp("", "shelley-secret-*")
	if err != nil {
		return "", err
	}
	defer func() {
		if cerr := f.Close(); err == nil {
			err = cerr
		}
		if err != nil {
			os.Remove(f.Name())
		}
	}()
	req, err := http.NewRequest(http.MethodPost, base+"/api/secrets/"+url.PathEscape(id)+"/reveal", nil)
	if err != nil {
		return "", err
	}
	req.Header.Set("X-Shelley-Request", "1")
	resp, err := hc.Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		b, _ := io.ReadAll(io.LimitReader(resp.Body, 4<<10))
		return "", errors.New(cmp.Or(strings.TrimSpace(string(b)), resp.Status))
	}
	var out struct {
		Value string `json:"value"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		return "", err
	}
	if _, err := f.WriteString(out.Value); err != nil {
		return "", err
	}
	return f.Name(), nil
}
