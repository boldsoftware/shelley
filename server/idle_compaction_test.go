package server

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"reflect"
	"strings"
	"sync"
	"testing"
	"testing/synctest"
	"time"

	"shelley.exe.dev/claudetool"
	"shelley.exe.dev/db"
	"shelley.exe.dev/llm"
	"shelley.exe.dev/llm/predictable"
	"shelley.exe.dev/models"
)

// Use real chat turns, not scheduleIdleCompaction: forgetting to arm the timer
// on normal completion must fail these tests too.
func newIdleCompactionHarness(t *testing.T) (*TestHarness, *ConversationManager) {
	t.Helper()
	return newIdlePolicyHarness(t, db.ConversationOptions{IdleCompactionMinutes: 3, IdleCompactionPrompt: "Keep the garden plan."})
}

func newIdlePolicyHarness(t *testing.T, opts db.ConversationOptions) (*TestHarness, *ConversationManager) {
	t.Helper()
	h := NewTestHarness(t)

	opts.DisableNotifications = true
	opts.ToolOverrides = map[string]string{claudetool.CompactInPlaceName: "on"}
	h.convOpts = &opts
	h.server.piDistillKeepRecentTokens = 1
	h.server.idleCompactionMinTokens = 1
	h.NewConversation("echo: keep the garden plan", "")
	synctest.Wait()
	h.Chat("echo: rosemary arrives tomorrow")
	synctest.Wait()
	cm, err := h.server.getOrCreateConversationManager(t.Context(), h.convID, "")
	if err != nil {
		t.Fatal(err)
	}
	if cm.IsAgentWorking() {
		t.Fatal("predictable turns did not finish")
	}
	var completed int
	for _, row := range listMessages(t, h.db, h.convID) {
		if row.Type != string(db.MessageTypeAgent) || row.LlmData == nil {
			continue
		}
		var msg llm.Message
		if err := json.Unmarshal([]byte(*row.LlmData), &msg); err != nil {
			t.Fatal(err)
		}
		if msg.EndOfTurn {
			completed++
		}
	}
	if completed != 2 {
		t.Fatalf("completed turns = %d, want 2", completed)
	}
	return h, cm
}

// idleCompactions counts the conversation's in-place compaction records.
func idleCompactions(t *testing.T, h *TestHarness) int {
	t.Helper()
	n := 0
	for _, row := range listMessages(t, h.db, h.convID) {
		if row.Type == string(db.MessageTypeInPlaceCompaction) {
			n++
		}
	}
	return n
}

func requireIdleCompactions(t *testing.T, h *TestHarness, want int) {
	t.Helper()
	if got := idleCompactions(t, h); got != want {
		t.Fatalf("compactions = %d, want %d", got, want)
	}
}

// TestIdleCompactionCompactsInPlaceOnce: once idle, the agent is nudged to
// compact its own context with compact_in_place. The nudge goes with the
// compaction, and the turn's end does not start another idle cycle.
func TestIdleCompactionCompactsInPlaceOnce(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		h, _ := newIdleCompactionHarness(t)
		defer stopActiveConversationLoops(h.server)

		<-time.After(3*time.Minute - time.Second)
		synctest.Wait()
		requireIdleCompactions(t, h, 0)
		<-time.After(time.Second)
		synctest.Wait()
		requireIdleCompactions(t, h, 1)

		requests := h.llm.GetRecentRequests()
		nudge := requests[len(requests)-2].Messages
		last := nudge[len(nudge)-1]
		if last.Content[0].Text != fmt.Sprintf(idleNudgePrompt, 3)+"\n\nKeep the garden plan." || !strings.Contains(last.Content[1].Text, "## Index") {
			t.Fatalf("idle turn did not start with the nudge and index: %+v", last)
		}
		after := requestDump(requests[len(requests)-1])
		if !strings.Contains(after, "IDLE NOTE") || strings.Contains(after, "has been idle") || !strings.Contains(after, "rosemary arrives tomorrow") {
			t.Fatalf("compacted context should hold the note and the recent part, without the nudge:\n%s", after)
		}

		request := h.llm.GetLastRequest()
		<-time.After(15 * time.Minute)
		synctest.Wait()
		requireIdleCompactions(t, h, 1)
		if h.llm.GetLastRequest() != request {
			t.Fatal("the idle turn's end started another idle cycle")
		}
	})
}

func TestIdleCompactionNewTurnRestartsDeadline(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		h, cm := newIdleCompactionHarness(t)
		defer stopActiveConversationLoops(h.server)
		compactions := idleCompactions(t, h)
		cm.mu.Lock()
		stale := cm.idleCompactionVersion
		cm.mu.Unlock()

		<-time.After(2 * time.Minute)
		h.Chat("delay: 120")
		synctest.Wait()
		if !cm.IsAgentWorking() {
			t.Fatal("expected a turn in progress")
		}
		// The previous deadline passes while the new turn is still running.
		<-time.After(time.Minute)
		synctest.Wait()
		requireIdleCompactions(t, h, compactions)
		<-time.After(time.Minute)
		synctest.Wait()
		if cm.IsAgentWorking() {
			t.Fatal("delayed turn did not finish")
		}
		// Simulate an already-dispatched old callback, now that busy checks
		// cannot hide a missing version check.
		if err := h.server.compactIdleConversation(cm, stale); err != nil {
			t.Fatal(err)
		}
		synctest.Wait()
		requireIdleCompactions(t, h, compactions)
		<-time.After(3*time.Minute - time.Second)
		synctest.Wait()
		requireIdleCompactions(t, h, compactions)
		<-time.After(time.Second)
		synctest.Wait()
		requireIdleCompactions(t, h, compactions+1)
	})
}

func TestIdleCompactionActivityAndSettingsInvalidateCallback(t *testing.T) {
	for _, activity := range []string{"reset", "stop", "disable policy", "queued message", "transcription"} {
		t.Run(activity, func(t *testing.T) {
			synctest.Test(t, func(t *testing.T) {
				h, cm := newIdleCompactionHarness(t)
				defer stopActiveConversationLoops(h.server)
				compactions := idleCompactions(t, h)
				cm.mu.Lock()
				stale := cm.idleCompactionVersion
				cm.mu.Unlock()

				switch activity {
				case "reset":
					cm.ResetLoop()
				case "stop":
					if err := cm.CancelConversation(t.Context()); err != nil {
						t.Fatal(err)
					}
				case "disable policy":
					// Disabling the policy must invalidate an already dispatched callback.
					if _, err := cm.ApplySettings(t.Context(), h.server.getModelList(), func(settings ConversationSettings) (ConversationSettings, error) {
						settings.IdleCompactionMinutes = 0
						return settings, nil
					}); err != nil {
						t.Fatal(err)
					}
				case "queued message":
					// Hold draining without BeginDistillingSetup, which would
					// itself invalidate the callback and mask a queue bug.
					cm.SetDistilling(true)
					if err := cm.QueueMessage(t.Context(), h.server, "predictable", llm.UserStringMessage("echo: queued")); err != nil {
						t.Fatal(err)
					}
					if _, err := h.db.ClearQueuedMessages(t.Context(), h.convID); err != nil {
						t.Fatal(err)
					}
					cm.SetDistilling(false)
				case "transcription":
					if _, err := cm.QueueTranscription(t.Context(), h.server, db.QueuedMessage{
						ID: "audio", Kind: db.QueuedMessageKindTranscription, State: db.QueuedMessageStateWorking,
						CreatedAt: time.Now(), Model: "predictable",
						Transcription: &db.QueuedTranscription{MediaPath: "/unused/audio.webm"},
					}); err != nil {
						t.Fatal(err)
					}
					if _, err := h.db.ClearQueuedMessages(t.Context(), h.convID); err != nil {
						t.Fatal(err)
					}
				}
				synctest.Wait()
				// No work remains to trip eligibility checks: cancellation
				// must invalidate callbacks even after the activity is gone.
				if err := h.server.compactIdleConversation(cm, stale); err != nil {
					t.Fatal(err)
				}
				synctest.Wait()
				<-time.After(6 * time.Minute)
				synctest.Wait()
				requireIdleCompactions(t, h, compactions)
			})
		})
	}
}

func TestIdleCompactionNothingToCompactIsOneShotNoOp(t *testing.T) {
	t.Run("short context", func(t *testing.T) {
		synctest.Test(t, func(t *testing.T) {
			h, cm := newIdleCompactionHarness(t)
			defer stopActiveConversationLoops(h.server)
			cm.keepRecentTokens = defaultPiDistillSettings.keepRecentTokens
			compactions := idleCompactions(t, h)
			before := len(listMessages(t, h.db, h.convID))
			<-time.After(3 * time.Minute)
			synctest.Wait()
			requireIdleCompactions(t, h, compactions)
			if got := len(listMessages(t, h.db, h.convID)); got != before {
				t.Fatalf("short context wrote %d extra messages", got-before)
			}

			// Make the existing context eligible. A no-op must not have armed
			// retries; only another completed turn may schedule compaction.
			cm.keepRecentTokens = 1
			<-time.After(9 * time.Minute)
			synctest.Wait()
			requireIdleCompactions(t, h, compactions)
			h.Chat("echo: another garden update")
			synctest.Wait()
			<-time.After(3 * time.Minute)
			synctest.Wait()
			requireIdleCompactions(t, h, compactions+1)
		})
	})
	t.Run("below the size threshold", func(t *testing.T) {
		synctest.Test(t, func(t *testing.T) {
			h, cm := newIdleCompactionHarness(t)
			defer stopActiveConversationLoops(h.server)
			cm.idleCompactionMinTokens = defaultIdleCompactionMinTokens
			compactions := idleCompactions(t, h)
			before := len(listMessages(t, h.db, h.convID))
			<-time.After(3 * time.Minute)
			synctest.Wait()
			requireIdleCompactions(t, h, compactions)
			if got := len(listMessages(t, h.db, h.convID)); got != before {
				t.Fatalf("small context wrote %d extra messages", got-before)
			}
		})
	})
}

func TestIdleCompactionRequiresEnabledPolicyAndTopLevel(t *testing.T) {
	for _, kind := range []string{"ordinary", "external chat disabled", "external chat enabled", "subagent child"} {
		t.Run(kind, func(t *testing.T) {
			synctest.Test(t, func(t *testing.T) {
				h := NewTestHarness(t)
				defer stopActiveConversationLoops(h.server)
				h.server.piDistillKeepRecentTokens = 1
				h.server.idleCompactionMinTokens = 1
				opts := db.ConversationOptions{DisableNotifications: true, ToolOverrides: map[string]string{claudetool.CompactInPlaceName: "on"}}
				conv, err := h.db.CreateConversation(t.Context(), nil, true, nil, nil, opts)
				if err != nil {
					t.Fatal(err)
				}
				switch kind {
				case "external chat disabled", "external chat enabled":
					if kind == "external chat enabled" {
						opts.IdleCompactionMinutes = 3
					}
					// Echo never invokes message_user; no external delivery.
					conv, err = h.db.CreateChannelConversation(t.Context(), "garden-chat", "/unused", "predictable", opts)
				case "subagent child":
					conv, err = h.db.CreateSubagentConversation(t.Context(), "gardener", conv.ConversationID, nil)
					if err == nil {
						opts.IdleCompactionMinutes = 3
						err = h.db.UpdateConversationOptions(t.Context(), conv.ConversationID, opts)
					}
				}
				if err != nil {
					t.Fatal(err)
				}
				h.convID = conv.ConversationID
				h.Chat("echo: first garden update")
				synctest.Wait()
				h.Chat("echo: second garden update")
				synctest.Wait()
				compactions := idleCompactions(t, h)
				<-time.After(3 * time.Minute)
				synctest.Wait()
				if kind == "external chat enabled" {
					compactions++
				}
				requireIdleCompactions(t, h, compactions)
			})
		})
	}
}

// TestIdleCompactionNeedsTheTool: a conversation with compact_in_place off,
// here by -no-tools, arms no idle compaction, whatever its profile says.
func TestIdleCompactionNeedsTheTool(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		h := NewTestHarness(t)
		defer stopActiveConversationLoops(h.server)
		h.convOpts = &db.ConversationOptions{DisableNotifications: true, DisableAllTools: true, IdleCompactionMinutes: 3}
		h.NewConversation("echo: keep the garden plan", "")
		synctest.Wait()
		cm, err := h.server.getOrCreateConversationManager(t.Context(), h.convID, "")
		if err != nil {
			t.Fatal(err)
		}
		cm.mu.Lock()
		armed := cm.idleCompactionTimer != nil
		cm.mu.Unlock()
		if armed {
			t.Fatal("idle compaction armed without compact_in_place")
		}
	})
}

// TestIdleCompactionUserWritesDuringIdleTurn: a message arriving during the
// idle turn reaches the model in it, and that exchange arms the next idle
// cycle.
func TestIdleCompactionUserWritesDuringIdleTurn(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		h, cm := newIdleCompactionHarness(t)
		defer stopActiveConversationLoops(h.server)
		h.llm.SetResponseDelay(30 * time.Second)
		<-time.After(3 * time.Minute)
		synctest.Wait()
		if !cm.IsAgentWorking() {
			t.Fatal("idle turn not running")
		}
		if w := postChat(t, h.server, h.convID, "echo: thyme too"); w.Code != http.StatusAccepted {
			t.Fatalf("chat during idle turn = %d: %s", w.Code, w.Body.String())
		}
		h.llm.SetResponseDelay(0)
		<-time.After(2 * time.Minute)
		synctest.Wait()
		if cm.IsAgentWorking() {
			t.Fatal("idle turn did not finish")
		}
		requireIdleCompactions(t, h, 1)
		// Predictable answers the tool result, not the message; a real model
		// sees both.
		if got := requestDump(h.llm.GetLastRequest()); !strings.Contains(got, "thyme too") {
			t.Fatalf("message sent during the idle turn did not reach the model:\n%s", got)
		}
		<-time.After(3 * time.Minute)
		synctest.Wait()
		requireIdleCompactions(t, h, 2)
	})
}

func TestIdleCompactionUsesCopiedProfileDelay(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		h, cm := newIdleCompactionHarness(t)
		defer stopActiveConversationLoops(h.server)
		compactions := idleCompactions(t, h)
		if _, err := h.server.changeSettings(t.Context(), cm, SettingsChange{IdleCompactionMinutes: new(7)}); err != nil {
			t.Fatal(err)
		}
		h.Chat("echo: use the longer quiet period")
		synctest.Wait()
		<-time.After(3 * time.Minute)
		synctest.Wait()
		requireIdleCompactions(t, h, compactions)
		<-time.After(4 * time.Minute)
		synctest.Wait()
		requireIdleCompactions(t, h, compactions+1)
	})
}

func cacheWarmUsage(t *testing.T, h *TestHarness) []llm.PurposedUsage {
	t.Helper()
	var warms []llm.PurposedUsage
	for _, row := range listMessages(t, h.db, h.convID) {
		if row.OtherUsageData == nil {
			continue
		}
		var usage []llm.PurposedUsage
		if err := json.Unmarshal([]byte(*row.OtherUsageData), &usage); err != nil {
			t.Fatal(err)
		}
		for _, u := range usage {
			if u.Purpose == "cache_warm" {
				warms = append(warms, u)
			}
		}
	}
	return warms
}

// A typical policy: compaction at 8 minutes, warm until then. A 5-minute
// cache needs exactly one refresh, shortly before it expires.
func TestCacheWarmRefreshesLastRequestBeforeIdleCompaction(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		h, _ := newIdlePolicyHarness(t, db.ConversationOptions{IdleCompactionMinutes: 8, CacheKeepWarmMinutes: 30})
		defer stopActiveConversationLoops(h.server)
		turn := h.llm.GetLastRequest()
		compactions := idleCompactions(t, h)

		<-time.After(4*time.Minute - time.Second)
		synctest.Wait()
		if got := len(h.llm.WarmRequests()); got != 0 {
			t.Fatalf("warmed %d times before the cache neared expiry", got)
		}
		<-time.After(time.Second)
		synctest.Wait()
		warms := h.llm.WarmRequests()
		if len(warms) != 1 {
			t.Fatalf("warms = %d, want 1", len(warms))
		}
		if !reflect.DeepEqual(warms[0].Messages, turn.Messages) || !reflect.DeepEqual(warms[0].System, turn.System) ||
			!reflect.DeepEqual(warms[0].Tools, turn.Tools) || warms[0].OnStream != nil {
			t.Fatal("warm did not replay the last turn's provider prefix")
		}
		if usage := cacheWarmUsage(t, h); len(usage) != 1 || usage[0].CacheReadInputTokens != 1024 {
			t.Fatalf("warm usage not recorded: %+v", usage)
		}

		// The refreshed cache outlives compaction, which ends warming.
		<-time.After(4 * time.Minute)
		synctest.Wait()
		requireIdleCompactions(t, h, compactions+1)
		<-time.After(30 * time.Minute)
		synctest.Wait()
		if got := len(h.llm.WarmRequests()); got != 1 {
			t.Fatalf("warms = %d after compaction, want 1", got)
		}
	})
}

func TestCacheWarmRestartsWithNewTurnAndEndsWithWindow(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		h, _ := newIdlePolicyHarness(t, db.ConversationOptions{IdleCompactionMinutes: 60, CacheKeepWarmMinutes: 20})
		defer stopActiveConversationLoops(h.server)

		<-time.After(3 * time.Minute)
		h.Chat("echo: water the basil")
		synctest.Wait()
		<-time.After(4*time.Minute - time.Second)
		synctest.Wait()
		if got := len(h.llm.WarmRequests()); got != 0 {
			t.Fatalf("stale turn warmed %d times", got)
		}
		<-time.After(time.Second)
		synctest.Wait()
		warms := h.llm.WarmRequests()
		if len(warms) != 1 || !strings.Contains(fmt.Sprint(warms[0].Messages), "water the basil") {
			t.Fatal("first warm did not replay the newest turn")
		}
		// Refreshes at 4, 8, 12 and 16 minutes; the last one lasts past 20.
		<-time.After(time.Hour)
		synctest.Wait()
		if got := len(h.llm.WarmRequests()); got != 4 {
			t.Fatalf("warms = %d, want 4", got)
		}
		if got := len(cacheWarmUsage(t, h)); got != 4 {
			t.Fatalf("recorded warms = %d, want 4", got)
		}
	})
}

// A cache that outlives the window (e.g. OpenAI's 30 minutes) is never warmed.
func TestCacheWarmSkipsCacheThatOutlivesWindow(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		h, _ := newIdlePolicyHarness(t, db.ConversationOptions{IdleCompactionMinutes: 60, CacheKeepWarmMinutes: 5})
		defer stopActiveConversationLoops(h.server)
		<-time.After(time.Hour)
		synctest.Wait()
		if got := len(h.llm.WarmRequests()); got != 0 {
			t.Fatalf("warms = %d, want 0", got)
		}
	})
}

func newCacheWarmSnapshotHarness(t *testing.T, service llm.Service) (*TestHarness, *models.Manager, models.Built) {
	t.Helper()
	h := NewTestHarness(t)
	built := models.Built{ID: "predictable", Service: service, ServiceRevision: 1}
	registry, err := models.NewManager(&models.Config{Models: []models.Built{built}})
	if err != nil {
		t.Fatal(err)
	}
	h.server.llmManager = registry
	h.convOpts = &db.ConversationOptions{CacheKeepWarmMinutes: 20, DisableNotifications: true}
	t.Cleanup(func() { stopActiveConversationLoops(h.server) })
	return h, registry, built
}

func TestCacheWarmConnectionChanges(t *testing.T) {
	for _, change := range []string{"unchanged", "label", "other model", "rotated", "removed"} {
		t.Run(change, func(t *testing.T) {
			synctest.Test(t, func(t *testing.T) {
				old, next := predictable.NewService(), predictable.NewService()
				h, registry, built := newCacheWarmSnapshotHarness(t, old)
				h.NewConversation("echo: garden plan", "")
				synctest.Wait()
				built.Service = next
				replacement := []models.Built{built}
				stale := change == "rotated" || change == "removed"
				switch change {
				case "label":
					replacement[0].DisplayName = "Renamed"
				case "other model":
					replacement = append(replacement, models.Built{ID: "other", Service: next, ServiceRevision: 2})
				case "rotated":
					replacement[0].ServiceRevision = 2
				case "removed":
					replacement = nil
				}
				if err := registry.RefreshBuiltModels(replacement); err != nil {
					t.Fatal(err)
				}
				<-time.After(4 * time.Minute)
				synctest.Wait()
				want := 1
				if stale {
					want = 0
				}
				if got := len(old.WarmRequests()); got != want || len(next.WarmRequests()) != 0 {
					t.Fatalf("old warms=%d new warms=%d, want old=%d new=0", got, len(next.WarmRequests()), want)
				}
				if change == "rotated" {
					cm, err := h.server.getOrCreateConversationManager(t.Context(), h.convID, "")
					if err != nil {
						t.Fatal(err)
					}
					cm.ResetLoop()
					h.Chat("echo: fresh turn")
					synctest.Wait()
					<-time.After(4 * time.Minute)
					synctest.Wait()
					if len(next.WarmRequests()) != 1 || len(old.WarmRequests()) != 0 {
						t.Fatal("fresh turn did not warm exclusively through the new connection")
					}
				}
			})
		})
	}
}

type cacheWarmBlockedService struct {
	*predictable.Service
	blockDo          bool
	started, release chan struct{}
	startedOnce      sync.Once
}

func (s *cacheWarmBlockedService) Do(ctx context.Context, req *llm.Request) (*llm.Response, error) {
	if s.blockDo {
		s.startedOnce.Do(func() { close(s.started) })
		<-s.release
	}
	return s.Service.Do(ctx, req)
}

func (s *cacheWarmBlockedService) WarmCache(ctx context.Context, req *llm.Request) (llm.Usage, error) {
	if !s.blockDo {
		s.startedOnce.Do(func() { close(s.started) })
		<-s.release
	}
	return s.Service.WarmCache(ctx, req)
}

func TestCacheWarmConnectionChangesDuringRequests(t *testing.T) {
	for _, blockDo := range []bool{true, false} {
		t.Run(fmt.Sprintf("inference=%t", blockDo), func(t *testing.T) {
			synctest.Test(t, func(t *testing.T) {
				service := &cacheWarmBlockedService{
					Service: predictable.NewService(), blockDo: blockDo,
					started: make(chan struct{}), release: make(chan struct{}),
				}
				h, registry, built := newCacheWarmSnapshotHarness(t, service)
				h.NewConversation("echo: garden plan", "")
				synctest.Wait()
				cm, err := h.server.getOrCreateConversationManager(t.Context(), h.convID, "")
				if err != nil {
					t.Fatal(err)
				}
				if !blockDo {
					<-time.After(4 * time.Minute)
				}
				<-service.started
				timer := cm.cacheWarmTimer
				built.Service, built.ServiceRevision = predictable.NewService(), 2
				if err := registry.RefreshBuiltModels([]models.Built{built}); err != nil {
					t.Fatal(err)
				}
				close(service.release)
				synctest.Wait()
				if !blockDo && cm.cacheWarmTimer != timer {
					t.Fatal("in-flight warm rearmed after its connection changed")
				}
				<-time.After(30 * time.Minute)
				synctest.Wait()
				want := 0
				if !blockDo {
					want = 1
				}
				if len(service.WarmRequests()) != want || len(cacheWarmUsage(t, h)) != want {
					t.Fatalf("warms=%d recorded=%d, want %d", len(service.WarmRequests()), len(cacheWarmUsage(t, h)), want)
				}
			})
		})
	}
}
