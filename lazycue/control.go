package lazycue

import (
	"bytes"
	"cmp"
	"context"
	"encoding/base64"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"sync"
	"sync/atomic"

	"github.com/chromedp/cdproto"
	"github.com/chromedp/cdproto/browser"
	"github.com/chromedp/cdproto/cdp"
	"github.com/chromedp/cdproto/fetch"
	"github.com/chromedp/cdproto/page"
	"github.com/chromedp/cdproto/runtime"
	"github.com/chromedp/cdproto/target"
	"github.com/chromedp/chromedp"
	jsonv2 "github.com/go-json-experiment/json"
)

// control is lazycue's own CDP connection to the browser, beside chromedp's.
// On it lazycue attaches to every page as Chrome creates it, paused before it
// runs, and sets the page up as Launch says, so that a page a tab opens (a
// popup) starts out like the tabs do, as with Playwright. Its session stays
// attached for the page's life: it holds the page's emulation and init
// scripts and answers its dialogs. chromedp cannot do this, as it drops the
// messages of sessions it did not attach itself.
type control struct {
	conn   *chromedp.Conn
	launch Launch
	// popup is called, in order, for each page a page opens.
	popup func(id target.ID)
	// closed is called when a page is gone.
	closed func(id target.ID)

	wmu  sync.Mutex // serializes writes
	next atomic.Int64

	mu      sync.Mutex
	replies map[int64]chan *cdproto.Message
	pages   map[target.ID]*setup
	targets map[target.SessionID]target.ID
	// downloads are the suggested file names of the downloads begun since
	// the browser launched or download last took them.
	downloads []string
	lost      chan struct{} // closed when the connection is gone
}

// setup is a page's set-up, done once ready is closed.
type setup struct {
	ready chan struct{}
	st    *tabState
	err   error
}

// devtoolsURL catches the browser's DevTools URL in its output, which
// chromedp keeps to itself.
type devtoolsURL chan string

func (w devtoolsURL) Write(p []byte) (int, error) {
	if u, ok := strings.CutPrefix(string(p), "DevTools listening on "); ok {
		select {
		case w <- strings.TrimSpace(u):
		default:
		}
	}
	return len(p), nil
}

// dialControl connects to the browser at url and attaches to its pages, the
// open ones and those to come.
func dialControl(ctx context.Context, url string, launch Launch, popup, closed func(target.ID)) (*control, error) {
	conn, err := chromedp.DialContext(ctx, url)
	if err != nil {
		return nil, err
	}
	c := &control{
		conn:    conn,
		launch:  launch,
		popup:   popup,
		closed:  closed,
		replies: map[int64]chan *cdproto.Message{},
		pages:   map[target.ID]*setup{},
		targets: map[target.SessionID]target.ID{},
		lost:    make(chan struct{}),
	}
	go c.read()
	bctx := cdp.WithExecutor(ctx, session{c, ""})
	attach := target.SetAutoAttach(true, true).WithFlatten(true).WithFilter(target.Filter{{Type: "page"}, {Exclude: true}})
	if err := attach.Do(bctx); err != nil {
		c.close()
		return nil, err
	}
	// Report downloads, as Playwright does, but save none.
	if err := browser.SetDownloadBehavior(browser.SetDownloadBehaviorBehaviorDeny).WithEventsEnabled(true).Do(bctx); err != nil {
		c.close()
		return nil, err
	}
	return c, nil
}

// download takes the downloads begun so far if one of them is name's, and
// reports which had begun.
func (c *control) download(name string) (began []string, ok bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	began = c.downloads
	if !slices.Contains(began, name) {
		return began, false
	}
	c.downloads = nil
	return began, true
}

// close closes the connection and waits for read to stop, which unblocks
// whatever waits on an answer.
func (c *control) close() {
	c.conn.Close()
	<-c.lost
}

// page waits for page id to be set up and returns its state.
func (c *control) page(ctx context.Context, id target.ID) (*tabState, error) {
	s := c.setup(id)
	select {
	case <-s.ready:
		return s.st, s.err
	case <-ctx.Done():
		return nil, ctx.Err()
	}
}

// setup is page id's set-up, begun or not.
func (c *control) setup(id target.ID) *setup {
	c.mu.Lock()
	defer c.mu.Unlock()
	s, ok := c.pages[id]
	if !ok {
		s = &setup{ready: make(chan struct{})}
		c.pages[id] = s
	}
	return s
}

// read handles what the browser sends until the connection is gone.
func (c *control) read() {
	defer close(c.lost)
	for {
		var m cdproto.Message
		if err := c.conn.Read(context.Background(), &m); err != nil {
			return
		}
		if m.ID != 0 {
			c.mu.Lock()
			reply := c.replies[m.ID]
			delete(c.replies, m.ID)
			c.mu.Unlock()
			if reply != nil {
				reply <- &m
			}
			continue
		}
		ev, err := cdproto.UnmarshalMessage(&m, chromedp.DefaultUnmarshalOptions)
		if err != nil {
			continue
		}
		s := session{c, m.SessionID}
		switch ev := ev.(type) {
		case *target.EventAttachedToTarget:
			id := ev.TargetInfo.TargetID
			c.mu.Lock()
			c.targets[ev.SessionID] = id
			c.mu.Unlock()
			if ev.TargetInfo.OpenerID != "" {
				c.popup(id)
			}
			go c.setUp(session{c, ev.SessionID}, id, ev.WaitingForDebugger)
		case *target.EventDetachedFromTarget:
			c.mu.Lock()
			id := c.targets[ev.SessionID]
			delete(c.targets, ev.SessionID)
			delete(c.pages, id)
			c.mu.Unlock()
			c.closed(id)
		case *browser.EventDownloadWillBegin:
			c.mu.Lock()
			c.downloads = append(c.downloads, ev.SuggestedFilename)
			c.mu.Unlock()
		case *fetch.EventRequestPaused:
			// Off this loop: a handler may take its time.
			go func() {
				_ = s.Execute(context.Background(), fetch.CommandFulfillRequest, c.answer(ev), nil) // the page may be gone
			}()
		case *page.EventJavascriptDialogOpening:
			// Close dialogs as Playwright does when no test listens for
			// them: leave the page on beforeunload, cancel alerts, confirms
			// and prompts. Otherwise the page hangs on them.
			go s.Execute(context.Background(), page.CommandHandleJavaScriptDialog, page.HandleJavaScriptDialog(ev.Type == page.DialogTypeBeforeunload), nil) //nolint:errcheck // the page may be gone
		}
	}
}

// answer runs the Launch.Hosts handler of a request's origin on it. As
// Playwright does for routed requests, it answers CORS preflights itself,
// allowing what they ask, and lets a page of another origin read a response
// that does not say who may.
func (c *control) answer(ev *fetch.EventRequestPaused) *fetch.FulfillRequestParams {
	bad := func(err error) *fetch.FulfillRequestParams {
		return fetch.FulfillRequest(ev.RequestID, http.StatusBadRequest).WithBody(base64.StdEncoding.EncodeToString([]byte(err.Error())))
	}
	var body []byte
	for _, e := range ev.Request.PostDataEntries {
		b, err := base64.StdEncoding.DecodeString(e.Bytes)
		if err != nil {
			return bad(err)
		}
		body = append(body, b...)
	}
	r, err := http.NewRequest(ev.Request.Method, ev.Request.URL, bytes.NewReader(body))
	if err != nil {
		return bad(err)
	}
	// As a server's request has it.
	r.RequestURI = r.URL.RequestURI()
	for k, v := range ev.Request.Headers {
		r.Header.Set(k, fmt.Sprint(v))
	}
	w := httptest.NewRecorder()
	origin, host := r.Header.Get("Origin"), r.URL.Scheme+"://"+r.URL.Host
	// Only a preflight has this header: pages may not set it.
	method := r.Header.Get("Access-Control-Request-Method")
	preflight := r.Method == http.MethodOptions && method != ""
	if preflight {
		w.Header().Set("Access-Control-Allow-Origin", cmp.Or(origin, "*"))
		w.Header().Set("Access-Control-Allow-Methods", method)
		w.Header().Set("Access-Control-Allow-Credentials", "true")
		if h := r.Header.Get("Access-Control-Request-Headers"); h != "" {
			w.Header().Set("Access-Control-Allow-Headers", h)
		}
		w.WriteHeader(http.StatusNoContent)
	} else {
		c.launch.Hosts[host].ServeHTTP(w, r)
	}
	res := w.Result()
	if h := res.Header; !preflight && origin != "" && origin != host && h.Get("Access-Control-Allow-Origin") == "" {
		h.Set("Access-Control-Allow-Origin", origin)
		h.Set("Access-Control-Allow-Credentials", "true")
		h.Add("Vary", "Origin")
	}
	var headers []*fetch.HeaderEntry
	for k, vs := range res.Header {
		for _, v := range vs {
			headers = append(headers, &fetch.HeaderEntry{Name: k, Value: v})
		}
	}
	return fetch.FulfillRequest(ev.RequestID, int64(res.StatusCode)).
		WithResponseHeaders(headers).
		WithBody(base64.StdEncoding.EncodeToString(w.Body.Bytes()))
}

// setUp sets a page up as launch says and lets it run.
func (c *control) setUp(s session, id target.ID, paused bool) {
	st := &tabState{session: s}
	// Commands to a paused page may be answered only once it runs, so send
	// them all, then the one that lets it run, then wait for the answers.
	b := &batch{s: s}
	ctx, cancel := context.WithTimeout(context.Background(), defaultStepTimeout)
	defer cancel()
	err := setUpTab(cdp.WithExecutor(ctx, b), c.launch, st)
	// Even when set-up failed: no page may stay paused.
	if paused {
		b.Execute(ctx, runtime.CommandRunIfWaitingForDebugger, nil, nil) //nolint:errcheck // send errors come back from wait
	}
	if err == nil {
		err = b.wait(ctx)
	}
	set := c.setup(id)
	set.st, set.err = st, err
	close(set.ready)
}

// session is a session on the control connection; "" is the browser's.
type session struct {
	c  *control
	id target.SessionID
}

// Execute sends a command and waits for its answer, as a cdp.Executor.
func (s session) Execute(ctx context.Context, method string, params, res any) error {
	reply, err := s.send(ctx, method, params)
	if err != nil {
		return err
	}
	return reply(ctx, res)
}

// send sends a command and returns what waits for its answer.
func (s session) send(ctx context.Context, method string, params any) (func(ctx context.Context, res any) error, error) {
	var buf []byte
	if params != nil {
		var err error
		if buf, err = jsonv2.Marshal(params, chromedp.DefaultMarshalOptions); err != nil {
			return nil, err
		}
	}
	id := s.c.next.Add(1)
	ch := make(chan *cdproto.Message, 1)
	s.c.mu.Lock()
	s.c.replies[id] = ch
	s.c.mu.Unlock()
	s.c.wmu.Lock()
	err := s.c.conn.Write(ctx, &cdproto.Message{ID: id, SessionID: s.id, Method: cdproto.MethodType(method), Params: buf})
	s.c.wmu.Unlock()
	if err != nil {
		return nil, err
	}
	return func(ctx context.Context, res any) error {
		select {
		case m := <-ch:
			if m.Error != nil {
				return m.Error
			}
			if res == nil {
				return nil
			}
			return jsonv2.Unmarshal(m.Result, res, chromedp.DefaultUnmarshalOptions)
		case <-s.c.lost:
			return errors.New("lost the browser")
		case <-ctx.Done():
			return ctx.Err()
		}
	}, nil
}

// batch is a cdp.Executor that sends commands without waiting for their
// answers, which wait collects.
type batch struct {
	s       session
	methods []string
	replies []func(context.Context, any) error
}

func (b *batch) Execute(ctx context.Context, method string, params, res any) error {
	reply, err := b.s.send(ctx, method, params)
	if err != nil {
		return fmt.Errorf("%s: %w", method, err)
	}
	b.methods = append(b.methods, method)
	b.replies = append(b.replies, reply)
	return nil
}

// wait waits for the answers to the commands sent, and returns the first
// error among them, after the command's name.
func (b *batch) wait(ctx context.Context) error {
	var first error
	for i, reply := range b.replies {
		if err := reply(ctx, nil); err != nil && first == nil {
			first = fmt.Errorf("%s: %w", b.methods[i], err)
		}
	}
	return first
}
