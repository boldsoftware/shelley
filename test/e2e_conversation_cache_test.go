package test

import (
	"fmt"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"testing"
	"time"
)

// Ported from ui/e2e/conversation-cache.spec.ts.

// cacheHasFullHistory defines window.__hasFullHistory(id), which resolves
// to whether the conversation's IndexedDB row reports a complete cached
// history. Persistence is write-behind, so messages on screen do not mean
// the cache is on disk: a test that reloads or leaves to exercise the cache
// waits for this first.
const cacheHasFullHistory = `window.__hasFullHistory = async (id) => {
  const db = await new Promise((resolve, reject) => {
    const req = indexedDB.open('shelley-messages');
    req.onsuccess = () => resolve(req.result);
    req.onerror = () => reject(req.error);
  });
  try {
    if (!db.objectStoreNames.contains('conversation_meta')) return false;
    const row = await new Promise((resolve) => {
      const r = db.transaction('conversation_meta', 'readonly').objectStore('conversation_meta').get(id);
      r.onsuccess = () => resolve(r.result);
      r.onerror = () => resolve(undefined);
    });
    return !!(row && row.has_full_history);
  } finally {
    db.close();
  }
};`

// conversationCacheTerms explains the steps the descriptions share.
const conversationCacheTerms = `Terms used below:
- "Wait for message X" means: wait (allow 15s) until a [data-testid="message"] element whose text contains X is visible. Always scope such waits to [data-testid="message"]: the closed conversation drawer holds each conversation's latest text too, so a page-wide text match passes before the conversation has loaded.
- "Wait until <id> is cached" means: an eval of window.__hasFullHistory("<id>") (a function the page already defines) with expect "true" and a 15s timeout; it reads the conversation's IndexedDB row.
- "Select conversation <slug>" means, on this phone-sized screen: click button[aria-label="Open conversations"], wait for ".drawer.open" to be visible (allow 5s), click the ".conversation-title" element whose text is exactly <slug> (has_text <slug> with "exact": true), then wait until there is no ".drawer.open" element (count 0, allow 10s).
`

// cacheLoads registers call "mark", which notes how many requests the
// page has sent so far, and for each name and conversation ID calls
// "<name> full loads", the number of GET /api/conversation/<id> requests
// without last_sequence_id since the mark, and "<name> incremental loads",
// "none" or "some" such requests with it.
func (s *shelley) cacheLoads(ids map[string]string) {
	var mu sync.Mutex
	mark := 0
	s.call("mark", func() string {
		mu.Lock()
		defer mu.Unlock()
		mark = len(s.requestLog())
		return "marked"
	})
	loads := func(id string) (full, incremental int) {
		mu.Lock()
		since := s.requestLog()[mark:]
		mu.Unlock()
		for _, r := range since {
			method, uri, _ := strings.Cut(r, " ")
			u, err := url.Parse(uri)
			if err != nil || method != "GET" || u.Path != "/api/conversation/"+id {
				continue
			}
			if u.Query().Has("last_sequence_id") {
				incremental++
			} else {
				full++
			}
		}
		return full, incremental
	}
	for name, id := range ids {
		s.call(name+" full loads", func() string {
			full, _ := loads(id)
			return fmt.Sprint(full)
		})
		s.call(name+" incremental loads", func() string {
			if _, n := loads(id); n > 0 {
				return "some"
			}
			return "none"
		})
	}
}

// cacheSwitchSetup makes the conversations cache-one ("Hello") and
// cache-two ("hello").
func (s *shelley) cacheSwitchSetup() {
	s.initScript(cacheHasFullHistory)
	one := s.conversation("Hello", "cache-one")
	s.conversation("hello", "cache-two")
	s.cacheLoads(map[string]string{"one": one.ID})
}

func TestE2EConversationCacheSwitchUsesCache(t *testing.T) {
	e2e(t, useful(0), conversationCacheTerms+`Switching back to a conversation uses the cache: no new fetch on the second visit. The conversation cache-one answered "Hello! I'm Shelley, your AI assistant."; cache-two answered "Well, hi there!". Perform the steps in order.
1. Navigate to /c/cache-one and wait for the message input ([data-testid="message-input"]) to be visible (allow 30s).
2. Wait for message "Hello! I'm Shelley, your AI assistant.".
3. Select conversation cache-two, then wait for message "Well, hi there!".
4. Call "mark".
5. Select conversation cache-one, then wait for message "Hello! I'm Shelley, your AI assistant.".
6. Call "one full loads": it must return exactly "0". Call "one incremental loads": it must return exactly "none". Switching back loaded nothing from the server.`, func(s *shelley) {
		s.cacheSwitchSetup()
	})
}

func TestE2EConversationCacheStreamingUpdates(t *testing.T) {
	e2e(t, useful(0), conversationCacheTerms+`A cached conversation shows the right messages after streaming updates. The conversation cache-one answered "Hello! I'm Shelley, your AI assistant.". Perform the steps in order.
1. Navigate to /c/cache-one and wait for the message input ([data-testid="message-input"]) to be visible (allow 30s).
2. Wait for message "Hello! I'm Shelley, your AI assistant.".
3. Fill the message input with "echo: follow up message" and click the send button ([data-testid="send-button"]). Wait for the reply: a ".message-agent" [data-testid="message"] element whose text contains "follow up message" is visible (allow 15s). The user's own bubble contains that text too, so it does not count.
4. Call "create second": it creates the conversation cache-two, which answers "Well, hi there!".
5. Reload the page (to pick up the new conversation in the list) and wait for the message input to be visible (allow 30s).
6. Select conversation cache-two, then wait for message "Well, hi there!".
7. Select conversation cache-one. Wait for message "Hello! I'm Shelley, your AI assistant." and wait for the reply again (the ".message-agent" [data-testid="message"] element whose text contains "follow up message"): the cache has both the original and the follow-up.`, func(s *shelley) {
		s.initScript(cacheHasFullHistory)
		s.conversation("Hello", "cache-one")
		s.call("create second", func() string {
			s.conversation("hello", "cache-two")
			return "created"
		})
	})
}

func TestE2EConversationCacheNoSpinner(t *testing.T) {
	e2e(t, useful(0), conversationCacheTerms+`The cache serves messages at once, without a loading spinner. The conversation cache-one answered "Hello! I'm Shelley, your AI assistant."; cache-two answered "Well, hi there!". Perform the steps in order.
1. Navigate to /c/cache-one and wait for the message input ([data-testid="message-input"]) to be visible (allow 30s).
2. Wait for message "Hello! I'm Shelley, your AI assistant.".
3. Select conversation cache-two, then wait for message "Well, hi there!".
4. Select conversation cache-one.
5. Right away, there is no ".spinner" element (count 0), and a [data-testid="message"] element whose text contains "Hello! I'm Shelley, your AI assistant." is visible within 5s: it comes from the cache, so it must not wait on a fetch.`, func(s *shelley) {
		s.cacheSwitchSetup()
	})
}

// The regression this guards: metadata mutators, which App pumps for every
// conversation in the list on startup, used to mark conversations hydrated
// without reading IndexedDB, so every reload re-downloaded the whole
// conversation.
func TestE2EConversationCacheReloadServesFromIndexedDB(t *testing.T) {
	e2e(t, useful(0), conversationCacheTerms+`A page reload serves history from the IndexedDB cache. The conversation cache-one (ID {{ID}}) answered "Hello! I'm Shelley, your AI assistant.". Perform the steps in order.
1. Navigate to /c/cache-one and wait for the message input ([data-testid="message-input"]) to be visible (allow 30s).
2. Wait for message "Hello! I'm Shelley, your AI assistant.".
3. Wait until {{ID}} is cached (the write-behind persistence landed).
4. Call "mark".
5. Reload the page, then wait for message "Hello! I'm Shelley, your AI assistant.".
6. Call "one full loads": it must return exactly "0". A complete cached history that the conversation list agrees is up to date needs no full download.
7. The cache diagnostics agree it was a cache hit: an eval of (() => { const s = window.__shelleyCache?.stats() ?? {}; return Object.keys(s).includes('load.served_from_cache') && (s['load.full_rest'] ?? 0) === 0; })() with expect "true".`, func(s *shelley) {
		s.initScript(cacheHasFullHistory)
		one := s.conversation("Hello", "cache-one")
		s.v("ID", one.ID)
		s.cacheLoads(map[string]string{"one": one.ID})
	})
}

// Leaving for about:blank disconnects the page before the archive, so the
// list stream cannot delete the cache: its persisted metadata still says
// archived:false when the conversation is opened again.
func TestE2EConversationCacheArchiveWhileClosed(t *testing.T) {
	e2e(t, useful(0), conversationCacheTerms+`Archiving while the tab is closed refreshes the cached metadata. The conversation cache-one (ID {{ID}}) answered "Hello! I'm Shelley, your AI assistant.". Perform the steps in order.
1. Navigate to /c/cache-one and wait for the message input ([data-testid="message-input"]) to be visible (allow 30s).
2. Wait for message "Hello! I'm Shelley, your AI assistant.".
3. Wait until {{ID}} is cached.
4. Navigate to about:blank (leaving the app, so the page is disconnected).
5. Call "archive": it archives cache-one through the API. Then call "mark".
6. Navigate to /c/cache-one. Call "one incremental loads" with expect "some" and a 15s timeout: the page asked the server for what changed.
7. Wait for message "Hello! I'm Shelley, your AI assistant.".
8. The conversation shows as archived: the message input ([data-testid="message-input"]) becomes hidden, and ".status-bar-archived" is visible.`, func(s *shelley) {
		s.initScript(cacheHasFullHistory)
		one := s.conversation("Hello", "cache-one")
		s.v("ID", one.ID)
		s.cacheLoads(map[string]string{"one": one.ID})
		s.call("archive", func() string {
			s.do("POST", "/api/conversation/"+one.ID+"/archive", nil)
			return "archived"
		})
	})
}

func TestE2EConversationCacheReloadRestoresPosition(t *testing.T) {
	e2e(t, useful(0), conversationCacheTerms+`A page reload restores a reading position that is not the bottom. The conversation at /c/{{SLUG}} (ID {{ID}}) is a generated 15-turn conversation. Perform the steps in order.
1. Navigate to /c/{{SLUG}} and wait for the message input ([data-testid="message-input"]) to be visible (allow 30s).
2. Wait (allow 15s) until there are more than 20 [data-testid="message"] elements (an eval with expect).
3. In one eval: set the ".messages-container" element's scrollTop to 1000 and dispatch a "scroll" Event on it.
4. Wait until localStorage.getItem("shelley_scroll_{{ID}}") is exactly "1000" (an eval with expect).
5. Reload the page. Wait for the message input to be visible (allow 30s), and (allow 15s) until there are more than 20 [data-testid="message"] elements.
6. Wait until the ".messages-container" element's scrollTop is greater than 900 and less than 1100 (an eval with expect "true"): the reading position came back.`, func(s *shelley) {
		c := s.loremipsum("15")
		s.v("SLUG", c.Slug)
		s.v("ID", c.ID)
	})
}

// cacheCaptureEventSources keeps every EventSource the page opens in
// window.__testEventSources.
const cacheCaptureEventSources = `{
  const NativeEventSource = window.EventSource;
  const sources = [];
  Object.defineProperty(window, '__testEventSources', { value: sources });
  Object.defineProperty(window, 'EventSource', {
    configurable: true,
    value: class extends NativeEventSource {
      constructor(url, init) {
        super(url, init);
        sources.push(this);
      }
    },
  });
}`

func TestE2EConversationCacheStalledRefreshShowsHotHistory(t *testing.T) {
	e2e(t, useful(0), conversationCacheTerms+`A stalled incremental refresh still reveals the hot cached history, at the bottom. The generated 15-turn conversation at /c/{{SLUG}} has ID {{ID}}; the conversation cache-other (ID {{OTHER_ID}}) answered "Hello! I'm Shelley, your AI assistant.". The page keeps every EventSource it opens in window.__testEventSources. Perform the steps in order.
1. Navigate to /c/{{SLUG}} and wait for the message input ([data-testid="message-input"]) to be visible (allow 30s).
2. Wait (allow 15s) until there are more than 20 [data-testid="message"] elements. Wait until {{ID}} is cached.
3. In one eval: set the ".messages-container" element's scrollTop to 1000 and dispatch a "scroll" Event on it. Wait until localStorage.getItem("shelley_scroll_{{ID}}") is exactly "1000" (an eval with expect).
4. Select conversation cache-other, wait for message "Hello! I'm Shelley, your AI assistant.", and wait until {{OTHER_ID}} is cached.
5. Call "mark".
6. In one eval: close the last EventSource in window.__testEventSources (throw if there is none) and dispatch an "online" Event on window.
7. Call "new conversation": it makes a new conversation on the server, an event for the reconnected stream.
8. Call "other incremental loads" with expect "some" and a 15s timeout: the reconnect refreshed cache-other.
9. Call "hold tail": from now on the server's answers to {{SLUG}}'s incremental refreshes (with last_sequence_id) are held back.
10. Select conversation {{SLUG}}. Call "tail held" with expect "true": the refresh is in flight and stalled.
11. With the network tail still blocked, the complete hot-memory history is already usable and unobscured, at the latest content (an explicit pick from the list opens at the bottom rather than at the old reading position): wait (allow 15s) until there are more than 20 [data-testid="message"] elements; there is no ".conversation-loading-overlay" (count 0); wait until the ".messages-container" element's scrollHeight - clientHeight - scrollTop is less than 120 (an eval with expect "true"); there is no ".scroll-to-bottom-button" (count 0).
12. Call "release tail".`, func(s *shelley) {
		s.initScript(cacheHasFullHistory)
		s.initScript(cacheCaptureEventSources)
		c := s.loremipsum("15")
		other := s.conversation("Hello", "cache-other")
		s.v("SLUG", c.Slug)
		s.v("ID", c.ID)
		s.v("OTHER_ID", other.ID)
		s.cacheLoads(map[string]string{"other": other.ID})
		s.call("new conversation", func() string {
			s.postForm("/debug/loremipsum?json=1", url.Values{"size": {"1"}, "model": {"predictable"}})
			return "created"
		})
		var hold sync.Mutex
		holding := false
		s.call("hold tail", func() string {
			hold.Lock()
			defer hold.Unlock()
			holding = true
			return "holding"
		})
		held := s.signal("tail held")
		release := s.gate("release tail")
		s.route("GET /api/conversation/{id}", func(w http.ResponseWriter, r *http.Request) {
			hold.Lock()
			h := holding
			hold.Unlock()
			if h && r.PathValue("id") == c.ID && r.URL.Query().Has("last_sequence_id") {
				held.Store(true)
				if !release.wait(r) {
					return
				}
			}
			s.pass.ServeHTTP(w, r)
		})
	})
}

// With a complete cached history the page never downloads it again just to
// find the tail: it asks for ?last_sequence_id=N instead.
func TestE2EConversationCacheAddedWhileClosed(t *testing.T) {
	e2e(t, useful(0), conversationCacheTerms+`Messages added while the tab was closed arrive through an incremental fetch. The conversation cache-one (ID {{ID}}) answered "Hello! I'm Shelley, your AI assistant.". Perform the steps in order.
1. Navigate to /c/cache-one and wait for the message input ([data-testid="message-input"]) to be visible (allow 30s).
2. Wait for message "Hello! I'm Shelley, your AI assistant.".
3. Wait until {{ID}} is cached (navigating away before the write-behind persistence lands would leave nothing cached).
4. Navigate to about:blank (leaving the app, so the page is not streaming).
5. Call "add message": it sends "echo: added while closed" to cache-one through the API and waits until the server has it. Then call "mark".
6. Navigate to /c/cache-one. Wait for message "Hello! I'm Shelley, your AI assistant." and wait for message "added while closed": both the cached history and the new tail are shown.
7. Call "one incremental loads": it must return exactly "some". Call "one full loads": it must return exactly "0".`, func(s *shelley) {
		s.initScript(cacheHasFullHistory)
		one := s.conversation("Hello", "cache-one")
		s.v("ID", one.ID)
		s.cacheLoads(map[string]string{"one": one.ID})
		s.call("add message", func() string {
			s.sendChat(one.ID, "echo: added while closed")
			s.poll(30*time.Second, func() error {
				if !strings.Contains(mustJSON(messagesOf(s.conversationBody(one.ID))), "added while closed") {
					return fmt.Errorf("message not stored yet")
				}
				return nil
			})
			return "added"
		})
	})
}
