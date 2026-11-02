package server

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"sort"
	"strings"

	"shelley.exe.dev/db"
	"shelley.exe.dev/db/generated"
)

// handleDebugHistograms renders a page summarizing the size distribution of
// the conversations in the current database: messages per conversation,
// stored bytes per conversation, the distribution of generations (how many
// times conversations have been compacted), and message-type counts. It is
// the reusable, in-product version of the one-off analysis that informed the
// /debug/loremipsum size presets, so other users can see their own numbers.
func (s *Server) handleDebugHistograms(w http.ResponseWriter, r *http.Request) {
	stats, err := s.collectHistogramStats(r.Context())
	if err != nil {
		http.Error(w, "collect stats: "+err.Error(), http.StatusInternalServerError)
		return
	}
	if r.URL.Query().Get("json") == "1" {
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(stats)
		return
	}
	blob, err := json.Marshal(stats)
	if err != nil {
		http.Error(w, "marshal stats: "+err.Error(), http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "text/html")
	w.Header().Set("Cache-Control", "no-cache, no-store, must-revalidate")
	w.Write([]byte(strings.Replace(debugHistogramsHTML, "__STATS_JSON__", string(blob), 1)))
}

// histogramStats is the payload rendered by the histograms page.
type histogramStats struct {
	Conversations int   `json:"conversations"`
	Messages      int64 `json:"messages"`
	Bytes         int64 `json:"bytes"`

	// Per-conversation samples, one entry per conversation, used by the
	// client to draw histograms and let Vega-Lite bin them.
	MessagesPerConv []int64 `json:"messages_per_conv"`
	BytesPerConv    []int64 `json:"bytes_per_conv"`

	// Precomputed percentile summaries so the page has a table even without
	// running JS.
	MessagesPercentiles percentiles `json:"messages_percentiles"`
	BytesPercentiles    percentiles `json:"bytes_percentiles"`

	// GenerationCounts maps a generation number to the count of
	// conversations whose newest generation is exactly that value (i.e. how
	// many compactions they have undergone, +1).
	GenerationCounts []labelCount `json:"generation_counts"`

	// TypeCounts is the total number of messages of each type across the DB.
	TypeCounts []labelCount `json:"type_counts"`
}

type percentiles struct {
	Min  int64   `json:"min"`
	P50  int64   `json:"p50"`
	P90  int64   `json:"p90"`
	P95  int64   `json:"p95"`
	P99  int64   `json:"p99"`
	Max  int64   `json:"max"`
	Mean float64 `json:"mean"`
}

type labelCount struct {
	Label string `json:"label"`
	Count int64  `json:"count"`
}

func (s *Server) collectHistogramStats(ctx context.Context) (*histogramStats, error) {
	st := &histogramStats{}
	err := s.db.Pool().Rx(ctx, func(ctx context.Context, rx *db.Rx) error {
		q := generated.New(rx.Conn())
		// Per-conversation message count and stored byte size. Bytes counts
		// the JSON payload columns the client must load and render. Note this
		// counts all stored rows including messages from older generations
		// retained after compaction — i.e. on-disk size, not the active set.
		sizes, err := q.ListConversationMessageSizes(ctx)
		if err != nil {
			return err
		}
		for _, size := range sizes {
			st.MessagesPerConv = append(st.MessagesPerConv, size.Messages)
			st.BytesPerConv = append(st.BytesPerConv, size.Bytes)
			st.Messages += size.Messages
			st.Bytes += size.Bytes
		}

		// Generation distribution: bucket conversations by their current
		// generation. Restrict to conversations that actually have messages so
		// this reconciles with the message/bytes aggregation above (an empty
		// conversation has no size to report).
		generations, err := q.CountConversationsByGeneration(ctx)
		if err != nil {
			return err
		}
		for _, g := range generations {
			st.GenerationCounts = append(st.GenerationCounts, labelCount{Label: fmt.Sprintf("%d", g.CurrentGeneration), Count: g.Count})
		}

		// Message-type distribution across the whole DB.
		types, err := q.CountMessagesByTypeAll(ctx)
		if err != nil {
			return err
		}
		for _, typ := range types {
			st.TypeCounts = append(st.TypeCounts, labelCount{Label: typ.Type, Count: typ.Count})
		}
		return nil
	})
	if err != nil {
		return nil, err
	}

	st.Conversations = len(st.MessagesPerConv)
	st.MessagesPercentiles = computePercentiles(st.MessagesPerConv)
	st.BytesPercentiles = computePercentiles(st.BytesPerConv)
	return st, nil
}

// computePercentiles returns summary percentiles for a sample. It sorts a
// copy so the caller's slice ordering is preserved. Percentiles use a
// zero-based fractional index (floor), which is stable and monotonic for the
// small samples here.
func computePercentiles(sample []int64) percentiles {
	if len(sample) == 0 {
		return percentiles{}
	}
	s := make([]int64, len(sample))
	copy(s, sample)
	sort.Slice(s, func(i, j int) bool { return s[i] < s[j] })
	var sum int64
	for _, v := range s {
		sum += v
	}
	// pct returns the value at the p-th percentile using a zero-based
	// fractional index (floored).
	pct := func(p float64) int64 {
		if len(s) == 1 {
			return s[0]
		}
		rank := int(p / 100 * float64(len(s)-1))
		return s[rank]
	}
	return percentiles{
		Min:  s[0],
		P50:  pct(50),
		P90:  pct(90),
		P95:  pct(95),
		P99:  pct(99),
		Max:  s[len(s)-1],
		Mean: float64(sum) / float64(len(s)),
	}
}
