package server

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"

	"github.com/coder/websocket"
	"github.com/coder/websocket/wsjson"

	"shelley.exe.dev/dtach"
)

// ExecMessage is the message format for terminal websocket communication.
// Server -> client uses TermID in an "attached" message so the browser can
// remember the persistent session id across reloads.
type ExecMessage struct {
	Type   string `json:"type"`
	Data   string `json:"data,omitempty"`
	Cols   uint16 `json:"cols,omitempty"`
	Rows   uint16 `json:"rows,omitempty"`
	TermID string `json:"term_id,omitempty"`
}

// handleExecWS handles websocket connections that proxy to a persistent dtach
// session. Sessions are created on first attach with cmd= and persisted on
// disk so they survive page reloads and shelley restarts.
//
// Query params:
//   - term_id: existing session id to re-attach to (preferred)
//   - cmd:     command to start a new session (required if term_id missing)
//   - cwd:     working directory for new sessions
func (s *Server) handleExecWS(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()

	termID := r.URL.Query().Get("term_id")
	cmd := r.URL.Query().Get("cmd")
	cwd := r.URL.Query().Get("cwd")

	if termID == "" && cmd == "" {
		http.Error(w, "cmd or term_id parameter required", http.StatusBadRequest)
		return
	}

	conn, err := websocket.Accept(w, r, &websocket.AcceptOptions{
		CompressionMode: websocket.CompressionDisabled,
	})
	if err != nil {
		s.logger.Error("Failed to upgrade websocket", "error", err)
		return
	}
	defer conn.Close(websocket.StatusInternalError, "internal error")

	var initMsg ExecMessage
	if err := wsjson.Read(ctx, conn, &initMsg); err != nil {
		s.logger.Debug("Failed to read init message", "error", err)
		return
	}
	if initMsg.Type != "init" {
		conn.Close(websocket.StatusPolicyViolation, "expected init message")
		return
	}
	cols := initMsg.Cols
	rows := initMsg.Rows
	if cols == 0 {
		cols = 80
	}
	if rows == 0 {
		rows = 24
	}

	sess, dc, err := s.attachOrSpawn(termID, cmd, cwd, cols, rows)
	if err != nil {
		wsjson.Write(ctx, conn, ExecMessage{Type: "error", Data: err.Error()})
		conn.Close(websocket.StatusInternalError, "attach failed")
		return
	}
	defer dc.Close()

	// Tell the client which session it ended up on (especially important if it
	// was just spawned).
	if err := wsjson.Write(ctx, conn, ExecMessage{Type: "attached", TermID: sess.ID}); err != nil {
		return
	}

	// Push the up-to-date PTY size from the client side.
	_ = dc.SendResize(cols, rows)

	s.bridgeWS(ctx, conn, dc, sess.ID)
}

func (s *Server) attachOrSpawn(termID, cmd, cwd string, cols, rows uint16) (*TerminalSession, *dtach.Client, error) {
	unlock := s.terminals.LockAttach()
	defer unlock()
	if termID != "" {
		if sess := s.terminals.Get(termID); sess != nil {
			dc, err := dtach.Attach(sess.Socket)
			if err == nil {
				return sess, dc, nil
			}
			// Stale record — forget and fall through to spawning a new one if
			// the caller also gave us cmd.
			s.terminals.Forget(termID)
			if cmd == "" {
				return nil, nil, fmt.Errorf("terminal %s no longer running", termID)
			}
		} else if cmd == "" {
			return nil, nil, fmt.Errorf("unknown terminal id %s", termID)
		}
	}
	return s.terminals.Spawn(cmd, cwd, cols, rows)
}

// bridgeWS shuttles bytes between the browser websocket and the dtach client.
func (s *Server) bridgeWS(ctx context.Context, conn *websocket.Conn, dc *dtach.Client, termID string) {
	var exited bool

	// dtach -> websocket. When this goroutine returns, close the websocket so
	// the reader unblocks.
	dtachDone := make(chan struct{})
	go func() {
		defer close(dtachDone)
		for {
			t, payload, err := dc.Recv()
			if err != nil {
				if !errors.Is(err, io.EOF) {
					s.logger.Debug("dtach recv error", "error", err)
				}
				return
			}
			switch t {
			case dtach.MsgSnapshot, dtach.MsgOutput:
				if len(payload) == 0 {
					continue
				}
				if err := wsjson.Write(ctx, conn, ExecMessage{
					Type: "output",
					Data: base64.StdEncoding.EncodeToString(payload),
				}); err != nil {
					return
				}
			case dtach.MsgExit:
				code, _ := dtach.DecodeExit(payload)
				exited = true
				_ = wsjson.Write(ctx, conn, ExecMessage{Type: "exit", Data: fmt.Sprintf("%d", code)})
				return
			}
		}
	}()

	// When the dtach side ends, close the ws to unblock Read below.
	go func() {
		<-dtachDone
		if exited {
			s.terminals.Forget(termID)
			conn.Close(websocket.StatusNormalClosure, "process exited")
		} else {
			// Detach: socket dropped but session may still be running. We don't
			// kill it; the browser can reconnect later by term_id.
			conn.Close(websocket.StatusGoingAway, "detached")
		}
	}()

	// websocket -> dtach
	for {
		var msg ExecMessage
		if err := wsjson.Read(ctx, conn, &msg); err != nil {
			return
		}
		switch msg.Type {
		case "input":
			if msg.Data == "" {
				continue
			}
			if err := dc.SendInput([]byte(msg.Data)); err != nil {
				return
			}
		case "resize":
			if msg.Cols > 0 && msg.Rows > 0 {
				_ = dc.SendResize(msg.Cols, msg.Rows)
			}
		}
	}
}

// handleTerminalsList responds with the current set of persistent terminals.
func (s *Server) handleTerminalsList(w http.ResponseWriter, r *http.Request) {
	type dto struct {
		ID        string `json:"id"`
		Command   string `json:"command"`
		Cwd       string `json:"cwd"`
		CreatedAt string `json:"created_at"`
	}
	list := s.terminals.List()
	out := make([]dto, 0, len(list))
	for _, t := range list {
		out = append(out, dto{
			ID:        t.ID,
			Command:   t.Command,
			Cwd:       t.Cwd,
			CreatedAt: t.CreatedAt.Format("2006-01-02T15:04:05Z"),
		})
	}
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(out)
}

// handleTerminalDelete kills a session and removes its on-disk record.
func (s *Server) handleTerminalDelete(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	if id == "" {
		http.Error(w, "missing id", http.StatusBadRequest)
		return
	}
	if err := s.terminals.Kill(id); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}
