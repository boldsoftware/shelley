package server

import (
	"context"
	"net"
	"net/http"

	"shelley.exe.dev/db"
	"shelley.exe.dev/db/generated"
)

// Temporary messages stay out of the archive store (archiver.go), like
// temporary conversations, but one turn at a time
// (db/schema/082-temporary-messages.sql).
//
// A message is of temporary origin when the user sends it as temporary, when
// it comes over the local socket from a process of a temporary turn (one
// whose environment has SHELLEY_TEMPORARY=1: the bash tool exports it while
// its conversation's run is open or the conversation is temporary), when a
// subagent reports from a temporary turn, or when a background job a
// temporary message started finishes. Such a message opens its
// conversation's temporary run, and everything recorded in the conversation
// from then on is temporary: the agent's replies, tool calls and results,
// messages the user sends while that turn runs, notices, and so on, until
// the user types a message that is not temporary and starts a new turn.
//
// A conversation created by a temporary origin (a task, through the API) is
// a temporary conversation, and so is a subagent created while its parent's
// run is open.

// temporaryEnv is in a command's environment when it runs in a temporary turn.
const temporaryEnv = "SHELLEY_TEMPORARY=1"

type temporaryContextKey struct{}

// contextWithTemporary marks the message ctx sends as of temporary origin.
func contextWithTemporary(ctx context.Context) context.Context {
	return context.WithValue(ctx, temporaryContextKey{}, true)
}

func temporaryFromContext(ctx context.Context) bool {
	temporary, _ := ctx.Value(temporaryContextKey{}).(bool)
	return temporary
}

// peerContextKey carries the process at the other end of a local socket
// connection.
type peerContextKey struct{}

type socketPeer struct {
	pid int
	err error
}

// withSocketPeer is the socket server's ConnContext.
func withSocketPeer(ctx context.Context, c net.Conn) context.Context {
	uc, ok := c.(*net.UnixConn)
	if !ok {
		return ctx
	}
	pid, err := peerPID(uc)
	return context.WithValue(ctx, peerContextKey{}, socketPeer{pid, err})
}

// requestTemporary reports whether r sends a message of temporary origin:
// the user sent it as temporary, or a process of a temporary turn did over
// the local socket.
func requestTemporary(r *http.Request, sent bool) (bool, error) {
	if sent {
		return true, nil
	}
	peer, ok := r.Context().Value(peerContextKey{}).(socketPeer)
	if !ok {
		return false, nil
	}
	if peer.err != nil {
		return false, peer.err
	}
	return processTemporary(peer.pid)
}

// temporaryNow reports whether what the conversation does now is temporary:
// a run is open in it, or it is a temporary conversation.
func (s *Server) temporaryNow(ctx context.Context, conversationID string) (bool, error) {
	return s.db.QueriesRes(ctx, func(q *generated.Queries) (bool, error) {
		return q.ConversationTemporaryNow(ctx, conversationID)
	})
}

// commandTemporaryEnv is the temporary part of a bash command's environment.
func (s *Server) commandTemporaryEnv(ctx context.Context, conversationID string) ([]string, error) {
	temporary, err := s.temporaryNow(ctx, conversationID)
	if err != nil || !temporary {
		return nil, err
	}
	return []string{temporaryEnv}, nil
}

// turnStartMark is the mark of a message that starts a turn: ctx's origin,
// and whether the user typed it.
func turnStartMark(ctx context.Context, typed bool) db.TemporaryMark {
	switch {
	case temporaryFromContext(ctx):
		return db.TemporaryStart
	case typed:
		return db.TemporaryEnd
	}
	return db.TemporaryInherit
}

// queuedMark is the mark of a queued item's first message as it is
// recorded; only a turn's first message can end a run.
func queuedMark(qm db.QueuedMessage, typed, turnStart bool) db.TemporaryMark {
	switch {
	case qm.Temporary:
		return db.TemporaryStart
	case typed && turnStart:
		return db.TemporaryEnd
	}
	return db.TemporaryInherit
}

// TemporaryMessages is the reply of GET /api/conversation/{id}/temporary-messages.
type TemporaryMessages struct {
	// Ranges are the temporary messages' sequence ids, as inclusive
	// [from, to] ranges.
	Ranges [][2]int64 `json:"ranges"`
}

func (s *Server) handleTemporaryMessages(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	ids, err := s.db.QueriesRes(ctx, func(q *generated.Queries) ([]int64, error) {
		return q.ListTemporarySequenceIDs(ctx, r.PathValue("id"))
	})
	if err != nil {
		s.internalError(w, "Failed to list temporary messages", err)
		return
	}
	out := TemporaryMessages{Ranges: [][2]int64{}}
	for _, id := range ids {
		if n := len(out.Ranges); n > 0 && out.Ranges[n-1][1] == id-1 {
			out.Ranges[n-1][1] = id
			continue
		}
		out.Ranges = append(out.Ranges, [2]int64{id, id})
	}
	writeJSON(w, http.StatusOK, out)
}
