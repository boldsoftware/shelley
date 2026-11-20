package client

import (
	"bytes"
	"cmp"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"math"
	"mime"
	"net/http"
	"net/url"
	"os"
	"reflect"
	"slices"
	"strings"
	"text/tabwriter"
	"time"

	"github.com/dustin/go-humanize"
	sdk "github.com/modelcontextprotocol/go-sdk/mcp"

	"shelley.exe.dev/mcp"
)

const mcpUsage = `Usage: shelley mcp [-url URL] <command>

Use the MCP servers registered with Shelley.

Commands:
  list                              List servers (without connecting to them)
  list SERVER                       List SERVER's tools as TypeScript-style signatures
  list SERVER.TOOL [-schema]        Show a tool; -schema adds its JSON schemas
  list -json [SERVER[.TOOL]]        Print servers, tools or a tool as JSON
  search [SERVER] QUERY...          Show tools matching every QUERY term (in a
                                    name, description or parameter); searches
                                    every server unless one is named. -json
                                    prints the matches as JSON
  call SERVER.TOOL [KEY=VALUE...]   Call a tool; VALUE is used as is for string
                                    parameters and parsed as JSON otherwise
  call SERVER.TOOL -                Call a tool with a JSON object of arguments on stdin
       [-json] [-timeout 1m]        -json prints the CallToolResult
  add NAME [-d DESC] [-H 'K: V']... URL
                                    Register a Streamable HTTP server
  rm NAME                           Remove a server
  auth NAME                         Show NAME's login state and login link
  restart NAME                      End this conversation's session with NAME

Flags may come before or after arguments. -url defaults to
unix://$SHELLEY_SOCKET, which Shelley sets for the commands it runs.
`

// RunMCP is the entry point for "shelley mcp [args...]".
func RunMCP(args []string) {
	os.Exit(runMCP(args, os.Stdin, os.Stdout, os.Stderr))
}

// usageError is a command line error: shelley mcp prints its usage and
// exits 2.
type usageError string

func (e usageError) Error() string { return string(e) }

// errToolError is a call whose result, already printed, is an error.
var errToolError = errors.New("the tool reported an error")

type mcpCLI struct {
	hc             *http.Client
	base           string
	stdin          io.Reader
	stdout, stderr io.Writer
}

func runMCP(args []string, stdin io.Reader, stdout, stderr io.Writer) int {
	err := (&mcpCLI{stdin: stdin, stdout: stdout, stderr: stderr}).run(args)
	var ue usageError
	switch {
	case err == nil:
		return 0
	case errors.Is(err, flag.ErrHelp):
		fmt.Fprint(stdout, mcpUsage)
		return 0
	case errors.Is(err, errToolError):
		return 1
	case errors.As(err, &ue):
		fmt.Fprintf(stderr, "shelley mcp: %v\n\n%s", err, mcpUsage)
		return 2
	}
	fmt.Fprintf(stderr, "shelley mcp: %v\n", err)
	return 1
}

func (c *mcpCLI) run(args []string) error {
	fs := flag.NewFlagSet("mcp", flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	serverURL := fs.String("url", defaultClientURL(), "")
	if err := fs.Parse(args); err != nil {
		return flagError(err)
	}
	if fs.NArg() == 0 {
		return usageError("no command")
	}
	cmd, ok := map[string]func([]string) error{
		"list":    c.list,
		"search":  c.search,
		"call":    c.call,
		"add":     c.add,
		"rm":      c.named("DELETE", ""),
		"auth":    c.auth,
		"restart": c.named("POST", "/restart"),
	}[fs.Arg(0)]
	if !ok {
		return usageError(fmt.Sprintf("unknown command %q", fs.Arg(0)))
	}
	var err error
	if c.hc, c.base, err = (&clientConfig{serverURL: *serverURL}).newHTTPClient(); err != nil {
		return err
	}
	return cmd(fs.Args()[1:])
}

func flagError(err error) error {
	if errors.Is(err, flag.ErrHelp) {
		return err
	}
	return usageError(err.Error())
}

// parse parses fs's flags wherever they are in args, and returns the other
// arguments, of which there must be between min and max.
func parse(fs *flag.FlagSet, args []string, min, max int) ([]string, error) {
	fs.SetOutput(io.Discard)
	var flags, pos []string
	for i := 0; i < len(args); i++ {
		a := args[i]
		if len(a) < 2 || a[0] != '-' {
			pos = append(pos, a)
			continue
		}
		flags = append(flags, a)
		name, _, hasValue := strings.Cut(strings.TrimLeft(a, "-"), "=")
		if f := fs.Lookup(name); f != nil && !hasValue && !isBoolFlag(f) && i+1 < len(args) {
			i++
			flags = append(flags, args[i])
		}
	}
	if err := fs.Parse(flags); err != nil {
		return nil, flagError(err)
	}
	if len(pos) < min || len(pos) > max {
		return nil, usageError("wrong number of arguments")
	}
	return pos, nil
}

func isBoolFlag(f *flag.Flag) bool {
	b, ok := f.Value.(interface{ IsBoolFlag() bool })
	return ok && b.IsBoolFlag()
}

// do makes a request to the MCP API, decodes the JSON response into out
// unless it's nil, and returns the response body.
func (c *mcpCLI) do(method, path string, body, out any) (http.Header, []byte, error) {
	var r io.Reader
	if body != nil {
		b, err := json.Marshal(body)
		if err != nil {
			return nil, nil, err
		}
		r = bytes.NewReader(b)
	}
	req, err := http.NewRequest(method, c.base+"/api/mcp/servers"+path, r)
	if err != nil {
		return nil, nil, err
	}
	req.Header.Set("X-Shelley-Conversation-Id", os.Getenv("SHELLEY_CONVERSATION_ID"))
	resp, err := c.hc.Do(req)
	if err != nil {
		return nil, nil, err
	}
	defer resp.Body.Close()
	b, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, nil, err
	}
	if resp.StatusCode >= 300 {
		return nil, nil, errors.New(cmp.Or(strings.TrimSpace(string(b)), resp.Status))
	}
	if out != nil {
		err = json.Unmarshal(b, out)
	}
	return resp.Header, b, err
}

func serverPath(name string) string { return "/" + url.PathEscape(name) }

func printJSON(w io.Writer, v any) error {
	b, err := json.MarshalIndent(v, "", "  ")
	if err == nil {
		_, err = fmt.Fprintf(w, "%s\n", b)
	}
	return err
}

// mcpServer is a server from GET /api/mcp/servers, without its headers so
// that list never prints their values.
type mcpServer struct {
	Name        string `json:"name"`
	URL         string `json:"url"`
	Description string `json:"description"`
	Auth        string `json:"auth"`
	LoginURL    string `json:"login_url,omitempty"`
}

func (c *mcpCLI) servers() ([]mcpServer, error) {
	var servers []mcpServer
	_, _, err := c.do("GET", "", nil, &servers)
	return servers, err
}

// mcpTools is the response of GET /api/mcp/servers/{name}/tools.
type mcpTools struct {
	Server mcp.ServerInfo `json:"server"`
	Tools  []*mcpTool     `json:"tools"`
}

func (c *mcpCLI) tools(server string) (*mcpTools, error) {
	var ts mcpTools
	_, _, err := c.do("GET", serverPath(server)+"/tools", nil, &ts)
	return &ts, err
}

// tool returns server's tool name.
func (ts *mcpTools) tool(server, name string) (*mcpTool, error) {
	if i := slices.IndexFunc(ts.Tools, func(t *mcpTool) bool { return t.Name == name }); i >= 0 {
		return ts.Tools[i], nil
	}
	return nil, fmt.Errorf("MCP server %q has no tool %q", server, name)
}

func (c *mcpCLI) list(args []string) error {
	fs := flag.NewFlagSet("list", flag.ContinueOnError)
	jsonOut := fs.Bool("json", false, "")
	schemas := fs.Bool("schema", false, "")
	pos, err := parse(fs, args, 0, 1)
	if err != nil {
		return err
	}
	if len(pos) == 0 {
		return c.listServers(*jsonOut)
	}
	server, toolName, isTool := strings.Cut(pos[0], ".")
	ts, err := c.tools(server)
	switch {
	case err != nil:
		return err
	case !isTool && *jsonOut:
		return printJSON(c.stdout, ts)
	case !isTool:
		return c.printTools(server, ts)
	}
	t, err := ts.tool(server, toolName)
	switch {
	case err != nil:
		return err
	case *jsonOut:
		return printJSON(c.stdout, t)
	}
	fmt.Fprint(c.stdout, t.doc())
	if !*schemas {
		return nil
	}
	var s struct {
		InputSchema  json.RawMessage `json:"inputSchema"`
		OutputSchema json.RawMessage `json:"outputSchema,omitempty"`
	}
	if err := json.Unmarshal(t.raw, &s); err != nil {
		return err
	}
	fmt.Fprintln(c.stdout)
	return printJSON(c.stdout, s)
}

func (c *mcpCLI) listServers(jsonOut bool) error {
	servers, err := c.servers()
	if err != nil {
		return err
	}
	if jsonOut {
		return printJSON(c.stdout, servers)
	}
	tw := tabwriter.NewWriter(c.stdout, 0, 4, 2, ' ', 0)
	fmt.Fprintln(tw, "NAME\tAUTH\tURL\tDESCRIPTION")
	for _, s := range servers {
		auth := cmp.Or(strings.ReplaceAll(s.Auth, "_", " "), "-")
		fmt.Fprintf(tw, "%s\t%s\t%s\t%s\n", s.Name, auth, s.URL, s.Description)
	}
	return tw.Flush()
}

func (c *mcpCLI) printTools(server string, ts *mcpTools) error {
	info := ts.Server
	name := cmp.Or(info.Title, info.Name)
	if info.Version != "" {
		name += " (v" + strings.TrimPrefix(info.Version, "v") + ")"
	}
	var b strings.Builder
	fmt.Fprintf(&b, "%s: %s, %d tools\n", server, name, len(ts.Tools))
	if instr := strings.TrimSpace(info.Instructions); instr != "" {
		for _, l := range strings.Split(instr, "\n") {
			b.WriteString(strings.TrimRight("// "+l, " \t\r") + "\n")
		}
	}
	for _, t := range ts.Tools {
		b.WriteString("\n" + t.doc())
	}
	_, err := io.WriteString(c.stdout, b.String())
	return err
}

// searchMatch is a tool that matched a search, with its server.
type searchMatch struct {
	Server string   `json:"server"`
	Tool   *mcpTool `json:"tool"`
}

// search prints the tools whose documentation contains every query term. It
// takes an optional leading SERVER argument (a term that names a server, or
// "SERVER" when the only argument) to limit the search to one server;
// otherwise it searches every registered server.
func (c *mcpCLI) search(args []string) error {
	fs := flag.NewFlagSet("search", flag.ContinueOnError)
	jsonOut := fs.Bool("json", false, "")
	pos, err := parse(fs, args, 1, math.MaxInt)
	if err != nil {
		return err
	}
	servers, err := c.servers()
	if err != nil {
		return err
	}
	// A leading argument that names a server, when it isn't the only argument,
	// limits the search to that server.
	terms := pos
	if len(pos) > 1 {
		if j := slices.IndexFunc(servers, func(s mcpServer) bool { return s.Name == pos[0] }); j >= 0 {
			servers, terms = servers[j:j+1], pos[1:]
		}
	}
	for j := range terms {
		terms[j] = strings.ToLower(terms[j])
	}
	matches := []searchMatch{}
	for _, s := range servers {
		ts, err := c.tools(s.Name)
		if err != nil {
			// A server that won't connect (needs a login, say) mustn't stop the
			// search of the others; note it and move on.
			fmt.Fprintf(c.stderr, "skipping %s: %v\n", s.Name, err)
			continue
		}
		for _, t := range ts.Tools {
			if toolMatches(t, terms) {
				matches = append(matches, searchMatch{Server: s.Name, Tool: t})
			}
		}
	}
	if *jsonOut {
		return printJSON(c.stdout, matches)
	}
	if len(matches) == 0 {
		fmt.Fprintf(c.stdout, "no tools match %s\n", strings.Join(terms, " "))
		return nil
	}
	var b strings.Builder
	prev := ""
	for _, m := range matches {
		if m.Server != prev {
			if prev != "" {
				b.WriteString("\n")
			}
			fmt.Fprintf(&b, "# %s\n\n", m.Server)
			prev = m.Server
		}
		fmt.Fprintf(&b, "%s.%s\n%s\n", m.Server, m.Tool.Name, m.Tool.doc())
	}
	_, err = io.WriteString(c.stdout, b.String())
	return err
}

// toolMatches reports whether t's documentation contains every term (already
// lowercased).
func toolMatches(t *mcpTool, terms []string) bool {
	hay := strings.ToLower(t.Name + "\n" + t.Title + "\n" + t.doc())
	for _, term := range terms {
		if !strings.Contains(hay, term) {
			return false
		}
	}
	return true
}

func (c *mcpCLI) call(args []string) error {
	fs := flag.NewFlagSet("call", flag.ContinueOnError)
	jsonOut := fs.Bool("json", false, "")
	timeout := fs.Duration("timeout", time.Minute, "")
	pos, err := parse(fs, args, 1, math.MaxInt)
	if err != nil {
		return err
	}
	sel, pairs := pos[0], pos[1:]
	server, toolName, ok := strings.Cut(sel, ".")
	if !ok {
		return usageError(fmt.Sprintf("%q is not SERVER.TOOL", sel))
	}
	var arguments any = map[string]any{}
	switch {
	case slices.Equal(pairs, []string{"-"}):
		// The server checks the arguments, so there's no need to fetch the tool.
		b, err := io.ReadAll(c.stdin)
		if err != nil {
			return err
		}
		var m map[string]json.RawMessage
		if json.Unmarshal(b, &m) != nil || m == nil {
			return errors.New("the arguments on stdin must be a JSON object")
		}
		arguments = m
	case slices.Contains(pairs, "-"):
		return usageError("give arguments as KEY=VALUE or as JSON on stdin (-), not both")
	case len(pairs) > 0:
		ts, err := c.tools(server)
		if err != nil {
			return err
		}
		t, err := ts.tool(server, toolName)
		if err != nil {
			return err
		}
		if arguments, err = t.arguments(sel, pairs); err != nil {
			return err
		}
	}
	var res sdk.CallToolResult
	header, body, err := c.do("POST", serverPath(server)+"/call", map[string]any{
		"tool": toolName, "arguments": arguments, "timeout_ms": timeout.Milliseconds(),
	}, &res)
	if err != nil {
		return err
	}
	if header.Get("X-Shelley-Mcp-App") == "shown" {
		fmt.Fprintln(c.stderr, "(This conversation shows the result in the tool's interactive view.)")
	}
	switch {
	case *jsonOut:
		err = printJSON(c.stdout, json.RawMessage(body))
	case res.IsError:
		err = printResult(c.stderr, &res)
	default:
		err = printResult(c.stdout, &res)
	}
	if err == nil && res.IsError {
		err = errToolError
	}
	return err
}

func (c *mcpCLI) add(args []string) error {
	fs := flag.NewFlagSet("add", flag.ContinueOnError)
	desc := fs.String("d", "", "")
	var hs multiFlag
	fs.Var(&hs, "H", "")
	pos, err := parse(fs, args, 2, 2)
	if err != nil {
		return err
	}
	headers := map[string]string{}
	for _, h := range hs {
		k, v, ok := strings.Cut(h, ":")
		if !ok {
			return usageError(fmt.Sprintf("header %q is not 'K: V'", h))
		}
		headers[strings.TrimSpace(k)] = strings.TrimSpace(v)
	}
	_, _, err = c.do("POST", "", map[string]any{"name": pos[0], "url": pos[1], "description": *desc, "headers": headers}, nil)
	return err
}

// named returns a command that makes a request about the server it names.
func (c *mcpCLI) named(method, suffix string) func([]string) error {
	return func(args []string) error {
		pos, err := parse(flag.NewFlagSet("", flag.ContinueOnError), args, 1, 1)
		if err == nil {
			_, _, err = c.do(method, serverPath(pos[0])+suffix, nil, nil)
		}
		return err
	}
}

func (c *mcpCLI) auth(args []string) error {
	pos, err := parse(flag.NewFlagSet("", flag.ContinueOnError), args, 1, 1)
	if err != nil {
		return err
	}
	servers, err := c.servers()
	if err != nil {
		return err
	}
	i := slices.IndexFunc(servers, func(s mcpServer) bool { return s.Name == pos[0] })
	switch {
	case i < 0:
		return fmt.Errorf("MCP server %q not found", pos[0])
	case servers[i].LoginURL == "":
		return fmt.Errorf("MCP server %q doesn't use Shelley's OAuth login", pos[0])
	}
	s := servers[i]
	fmt.Fprintf(c.stdout, "%s: %s\n", s.Name, cmp.Or(strings.ReplaceAll(s.Auth, "_", " "), "not logged in"))
	if s.Auth != "logged_in" {
		fmt.Fprintf(c.stdout, "Log in: %s\n", s.LoginURL)
	}
	return nil
}

// printResult prints a tool's result: text as is, binary content saved to
// files, and structured content as JSON unless a text block holds it.
func printResult(w io.Writer, res *sdk.CallToolResult) error {
	structuredShown := false
	for _, content := range res.Content {
		var line string
		var err error
		switch content := content.(type) {
		case *sdk.TextContent:
			line = content.Text
			var v any
			if res.StructuredContent != nil && json.Unmarshal([]byte(content.Text), &v) == nil && reflect.DeepEqual(v, res.StructuredContent) {
				structuredShown = true
			}
		case *sdk.ImageContent:
			line, err = save("image", content.Data, content.MIMEType)
		case *sdk.AudioContent:
			line, err = save("audio", content.Data, content.MIMEType)
		case *sdk.ResourceLink:
			line = fmt.Sprintf("[resource: %s (%s)]", content.URI, content.Name)
		case *sdk.EmbeddedResource:
			if r := content.Resource; r.Blob != nil {
				line, err = save("resource "+r.URI, r.Blob, r.MIMEType)
			} else {
				line = r.Text
			}
		}
		if err != nil {
			return err
		}
		if _, err := fmt.Fprintln(w, strings.TrimSuffix(line, "\n")); err != nil {
			return err
		}
	}
	if res.StructuredContent == nil || structuredShown {
		return nil
	}
	b, err := json.Marshal(res.StructuredContent)
	if err == nil {
		_, err = fmt.Fprintf(w, "%s\n", b)
	}
	return err
}

// save saves data to a temporary file and returns a line saying so.
func save(what string, data []byte, mimeType string) (string, error) {
	ext := ""
	if exts, _ := mime.ExtensionsByType(mimeType); len(exts) > 0 {
		ext = exts[0]
	}
	f, err := os.CreateTemp("", "shelley-mcp-*"+ext)
	if err != nil {
		return "", err
	}
	_, err = f.Write(data)
	if err := cmp.Or(err, f.Close()); err != nil {
		return "", err
	}
	return fmt.Sprintf("[%s saved to %s (%s, %s)]", what, f.Name(), mimeType, humanize.Bytes(uint64(len(data)))), nil
}
