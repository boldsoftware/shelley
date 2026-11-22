package test

import (
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/boldsoftware/shelley/lazycue"
)

// Ported from ui/e2e/tag-filter.spec.ts.

// tagFilterConv starts a conversation in cwd (the server's when empty)
// and tags it.
func (s *shelley) tagFilterConv(message, cwd string, tags ...string) convo {
	if cwd == "" {
		cwd = s.cwd
	}
	c := s.conversationWith(map[string]any{"message": message, "model": "predictable", "cwd": cwd}, "")
	s.tagFilterSetTags(c.ID, tags...)
	return c
}

// tagFilterSetTags replaces a conversation's tags.
func (s *shelley) tagFilterSetTags(id string, tags ...string) {
	if tags == nil {
		tags = []string{}
	}
	s.api("POST", "/api/conversation/"+id+"/tags", map[string]any{"tags": tags}, nil)
}

// How the spec's tests drive the drawer, on the Pixel 5 they ran on.
const tagFilterDrawer = `Conversation rows are ".conversation-item[data-conversation-id='<id>']" ("the row of <id>"). To "open the drawer": click the button "Open conversations", then wait until ".drawer" has the class "open" (eval, polled). To "open the search": click the button "Search conversations..." (the search is closed at first), then wait for the editor to be visible. The tag dropdown ("the panel") is data-testid "tag-filter-panel"; "the option T" is [data-testid="tag-filter-option"][data-tag="T"] and its count is its ".tag-filter-option-count". To "open the actions of" a row: hover the row, click the button "Actions" inside it, then wait for data-testid "conversation-actions-menu" to be visible; its items are role menuitem. `

// tagFilterTest runs a test of the spec's.
func tagFilterTest(t *testing.T, useful lazycue.Useful, description string, setup func(s *shelley)) {
	t.Helper()
	e2e(t, useful, description, setup)
}

// Alpha: tf-alpha, tf-shared; beta: tf-beta, tf-shared; gamma: tf-gamma.
// tf-alpha and tf-beta never co-occur, so tf-beta must not be offered once
// tf-alpha is selected. Playwright's tags carried a run-specific suffix,
// since its server was shared; each test here has its own.
func TestE2ETagFilterDropdown(t *testing.T) {
	tagFilterTest(t, useful(0), `tag: opens a dropdown, completes, ANDs, and narrows the offers. Conversation {{ALPHA}} is tagged tf-alpha and tf-shared, {{BETA}} tf-beta and tf-shared, and {{GAMMA}} (slug {{GAMMA_SLUG}}) tf-gamma. `+tagFilterDrawer+drawerQuery+drawerDirect+drawerSteps+`1. Navigate to /c/{{GAMMA_SLUG}} and wait up to 30s for data-testid "message-input" to be visible. 2. Open the drawer. 3. Open the search. 4. There is no panel (no dropdown until the tag: cue is typed). 5. Click the button "Add tag filter". 6. The query is "tag:". 7. There is no [data-conversation-query-token="tag"] inside the editor. 8. The panel is visible. 9. The count of the option tf-shared has text exactly "2", and that of the option tf-alpha exactly "1" (the size of the result set each tag would produce). 10. Set the query to "tag:shared". 11. The option tf-shared is visible, and there is no option tf-alpha. 12. Press Space in the editor (it accepts the highlighted match). 13. The query is "tag:tf-shared " (with the trailing space), and the editor's [data-conversation-query-token="tag"][data-query-raw="tag:tf-shared"] is visible. 14. The rows of {{ALPHA}} and {{BETA}} are visible, and the row of {{GAMMA}} is not visible (assert_not_visible). 15. Click the button "Add tag filter". 16. The query is "tag:tf-shared tag:". 17. The panel is visible; there is no option tf-gamma (it never co-occurs with tf-shared) and no option tf-shared (already selected), and the option tf-alpha is visible. 18. Click the option tf-alpha. 19. The query is "tag:tf-shared tag:tf-alpha ". 20. The editor holds exactly 2 [data-conversation-query-token="tag"]. 21. The row of {{ALPHA}} is visible and the row of {{BETA}} is not visible. 22. Type "temporary text" key by key (type action) into the focused editor. 23. The query is "tag:tf-shared tag:tf-alpha temporary text". 24. Press Escape. 25. The query is "tag:tf-shared tag:tf-alpha " (Escape cleared the free text and kept the tags). 26. The row of {{BETA}} is not visible. 27. Clear the query. 28. The rows of {{ALPHA}}, {{BETA}} and {{GAMMA}} are all visible.`, func(s *shelley) {
		alpha := s.tagFilterConv("tag filter alpha", "", "tf-alpha", "tf-shared")
		beta := s.tagFilterConv("tag filter beta", "", "tf-beta", "tf-shared")
		gamma := s.tagFilterConv("tag filter gamma", "", "tf-gamma")
		s.v("ALPHA", alpha.ID)
		s.v("BETA", beta.ID)
		s.v("GAMMA", gamma.ID)
		s.v("GAMMA_SLUG", gamma.Slug)
	})
}

func TestE2ETagFilterKeyboard(t *testing.T) {
	tagFilterTest(t, useful(0), `keyboard drives the dropdown, and Escape peels one layer at a time. Conversation {{KB}} (slug {{KB_SLUG}}) is tagged kb-only. `+tagFilterDrawer+drawerQuery+drawerDirect+drawerSteps+`Press keys with press_key on the focused editor. 1. Navigate to /c/{{KB_SLUG}} and wait up to 30s for data-testid "message-input" to be visible. 2. Open the drawer. 3. Open the search. 4. Set the query to "tag:kb-on". 5. The option kb-only is visible. 6. Press Enter (it takes the highlighted entry). 7. The query is "tag:kb-only ". 8. There is no panel. 9. Click the button "Add tag filter". 10. The query is "tag:kb-only tag:". 11. The panel is visible. 12. Press Escape. 13. There is no panel (Escape closes the dropdown first). 14. The query is still "tag:kb-only tag:". 15. The dropdown does not spring back while the term is unchanged: sleep 500ms, then there is still no panel. 16. Press Escape. 17. The query is "tag:kb-only " (the second Escape cleared the partial term). 18. Press Escape. 19. There is no ".drawer-search-input" (the third Escape closed the search box).`, func(s *shelley) {
		kb := s.tagFilterConv("keyboard tag conversation", "", "kb-only")
		s.v("KB", kb.ID)
		s.v("KB_SLUG", kb.Slug)
	})
}

func TestE2ETagFilterTextAndTags(t *testing.T) {
	tagFilterTest(t, useful(0), `tag terms and free text are independent predicates. Conversation {{HIT}} (slug {{HIT_SLUG}}), whose message is "searchable kumquat marker", is tagged sf-keep; {{OTHER}}, whose message is "searchable kumquat other", is tagged sf-drop. `+tagFilterDrawer+drawerQuery+drawerDirect+drawerSteps+`"Click the clear button" means click ".drawer-search-clear". 1. Navigate to /c/{{HIT_SLUG}} and wait up to 30s for data-testid "message-input" to be visible. 2. Open the drawer. 3. Open the search. 4. Set the query to "kumquat tag:sf-keep " (text and tag in one query: both must hold). 5. The row of {{HIT}} is visible and the row of {{OTHER}} is not visible (assert_not_visible). 6. Click the clear button, then set the query to "tag:sf-keep kumquat" (order does not matter). 7. The row of {{HIT}} is visible and the row of {{OTHER}} is not visible. 8. Click the clear button, then set the query to "zzz-no-such-conversation-zzz tag:sf-keep ". 9. A search that matches nothing is a search miss, not a filter miss: there is no data-testid "tag-filter-empty", and ".drawer-empty-state" contains the text "No matching conversations". 10. Click the clear button, then set the query to "kumquat marker tag:sf-drop ". 11. A search whose hits the tag then removes is a filter miss: data-testid "tag-filter-empty" is visible. 12. Click data-testid "tag-filter-empty-clear". 13. The query is "kumquat marker " (the free text survives; only the tag terms are dropped). 14. The row of {{HIT}} is visible.`, func(s *shelley) {
		hit := s.tagFilterConv("searchable kumquat marker", "", "sf-keep")
		other := s.tagFilterConv("searchable kumquat other", "", "sf-drop")
		s.v("HIT", hit.ID)
		s.v("HIT_SLUG", hit.Slug)
		s.v("OTHER", other.ID)
	})
}

// Playwright's slugs carried part of the conversation's ID; these are fixed,
// as each test has its own server.
func TestE2ETagFilterRenameRefetchesSearch(t *testing.T) {
	tagFilterTest(t, useful(0), `renaming a search hit refetches its FTS membership. Conversation {{ID}} is named fts-old-4f2a9c1e. `+tagFilterDrawer+drawerQuery+drawerDirect+drawerSteps+`1. Navigate to /c/fts-old-4f2a9c1e and wait up to 30s for data-testid "message-input" to be visible. 2. Open the drawer. 3. Open the search. 4. Set the query to "fts-old-4f2a9c1e". 5. The row of {{ID}} is visible. 6. Open the actions of the row of {{ID}} and click the menu item "Rename" in the menu (exact name). 7. Fill the row's ".drawer-rename-input" with "fts-new-4f2a9c1e" and press Enter in it. 8. There is no row of {{ID}} (the renamed conversation no longer matches the search). 9. Set the query to "fts-new-4f2a9c1e". 10. The row of {{ID}} is visible.`, func(s *shelley) {
		c := s.conversation("rename search hit", "fts-old-4f2a9c1e")
		s.v("ID", c.ID)
	})
}

// The conversation outside the repository runs in the parent of the
// server's working directory, as Playwright's ran in its worker
// directory's.
func TestE2ETagFilterChipsWithGitRepoGrouping(t *testing.T) {
	tagFilterTest(t, useful(0), `row tag chips write into the query and compose with git-repo grouping. Conversation {{IN}} (slug {{IN_SLUG}}) runs in the server's git repository and is tagged chip-tag; {{OUT}} runs outside any repository and is tagged chip-other. `+tagFilterDrawer+drawerQuery+drawerDirect+drawerSteps+`"The chip" is the element with data-testid "conversation-tag-chip" inside the row of {{IN}} whose text contains "chip-tag" (has_text). 1. Navigate to /c/{{IN_SLUG}} and wait up to 30s for data-testid "message-input" to be visible. 2. Open the drawer. 3. The chip is visible. 4. Click the chip (it opens the search box and writes the term). 5. The query is "tag:chip-tag ". 6. The row of {{IN}} is visible and the row of {{OUT}} is not visible (assert_not_visible). 7. Clicking a chip must not also navigate: the URL is exactly /c/{{IN_SLUG}} (assert_url value). 8. The chip's aria-pressed is "true". 9. Click the button "View options", then the button "Git Repo" (a tag-only query is a filter, not a search: the list keeps its grouping). 10. Exactly one ".conversation-group" holds the row of {{IN}}: assert_count of ".conversation-group:has(.conversation-item[data-conversation-id='{{IN}}'])" is 1. 11. That group's ".conversation-group-label" does not read "Other": eval with expect "true" of (() => { const l = document.querySelector(".conversation-group:has(.conversation-item[data-conversation-id='{{IN}}']) .conversation-group-label"); return l ? String(l.textContent.trim() !== "Other") : "no label"; })(). 12. Click the chip again (it removes its term). 13. The query is "". 14. The row of {{OUT}} is visible.`, func(s *shelley) {
		var diffs struct {
			GitRoot string `json:"gitRoot"`
		}
		s.api("GET", "/api/git/diffs?cwd=.", nil, &diffs)
		if diffs.GitRoot == "" {
			s.fail("no git root")
		}
		in := s.tagFilterConv("chip filter in repo", diffs.GitRoot, "chip-tag")
		out := s.tagFilterConv("chip filter elsewhere", filepath.Dir(s.cwd), "chip-other")
		s.v("IN", in.ID)
		s.v("IN_SLUG", in.Slug)
		s.v("OUT", out.ID)
	})
}

// The server's normalizeTags allows spaces, so `tag:in progress` unquoted
// would parse as the tag `in` plus the word `progress`.
func TestE2ETagFilterSpacedTagQuoted(t *testing.T) {
	tagFilterTest(t, useful(0), `a tag containing a space is quoted, and round-trips. Conversation {{SPACED}} (slug {{SPACED_SLUG}}) is tagged "sp tag"; {{PLAIN}} is tagged sp-plain. Call "add-quote-tag" tags {{SPACED}} with "sp tag" and 'sp" quote' (a tag holding a double quote). `+tagFilterDrawer+drawerQuery+drawerDirect+drawerSteps+`"The chip" is the element with data-testid "conversation-tag-chip" inside the row of {{SPACED}} whose text contains "sp tag" (has_text); "the quote chip" is the row of {{SPACED}}'s [data-testid="conversation-tag-chip"][data-tag='sp" quote']. 1. Navigate to /c/{{SPACED_SLUG}} and wait up to 30s for data-testid "message-input" to be visible. 2. Open the drawer. 3. Click the chip. 4. The query is 'tag:"sp tag" ' (the chip writes the quoted term). 5. The row of {{SPACED}} is visible and the row of {{PLAIN}} is not visible (assert_not_visible). 6. Click the chip again. 7. The query is "" (the whole quoted term is gone). 8. Set the query to 'tag:"sp t' (typing inside an open quote keeps narrowing instead of committing). 9. The option "sp tag" is visible. 10. Press Enter. 11. The query is 'tag:"sp tag" ' (completing from the dropdown quotes too). 12. The row of {{SPACED}} is visible and the row of {{PLAIN}} is not visible. 13. Call "add-quote-tag". 14. Clear the query. 15. Click the quote chip. 16. The query is 'tag:"sp\" quote" ' (the quote mark is backslash-escaped: the query is the 18 characters t a g : " s p \ " space q u o t e " space). 17. The row of {{SPACED}} is visible and the row of {{PLAIN}} is not visible. 18. Click the quote chip again. 19. The query is "".`, func(s *shelley) {
		spaced := s.tagFilterConv("spaced tag conversation", "", "sp tag")
		plain := s.tagFilterConv("spaced tag other", "", "sp-plain")
		s.v("SPACED", spaced.ID)
		s.v("SPACED_SLUG", spaced.Slug)
		s.v("PLAIN", plain.ID)
		s.call("add-quote-tag", func() string {
			s.tagFilterSetTags(spaced.ID, "sp tag", `sp" quote`)
			return "tagged"
		})
	})
}

func TestE2ETagFilterUntagged(t *testing.T) {
	tagFilterTest(t, useful(0), `is:untagged filters to untagged, in its own namespace. Conversation {{TAGGED}} is tagged ut-real, {{BARE}} (slug {{BARE_SLUG}}) has no tags, and {{LITERAL}} is tagged with a tag literally named "untagged" (the collision that is: avoids). `+tagFilterDrawer+drawerQuery+drawerDirect+drawerSteps+`1. Navigate to /c/{{BARE_SLUG}} and wait up to 30s for data-testid "message-input" to be visible. 2. Open the drawer. 3. Open the search. 4. Set the query to "tag:". 5. The panel is visible. 6. Click data-testid "tag-filter-untagged-option" (matched by test id, not by text: a tag named "untagged" exists too). 7. The query is "is:untagged ". 8. The row of {{BARE}} is visible; the rows of {{TAGGED}} and of {{LITERAL}} (which has a tag) are not visible (assert_not_visible). 9. Set the query to "tag:untagged " (the tag means the opposite: only the conversation carrying it). 10. The row of {{LITERAL}} is visible and the row of {{BARE}} is not visible. 11. Set the query to "is:untagged tag:ut-real " (contradictory: nothing has a tag and no tags). 12. Data-testid "tag-filter-empty" is visible. 13. Click data-testid "tag-filter-empty-clear". 14. The query is "" (its clear action drops both terms).`, func(s *shelley) {
		tagged := s.tagFilterConv("untagged probe tagged", "", "ut-real")
		bare := s.tagFilterConv("untagged probe bare", "")
		literal := s.tagFilterConv("untagged probe literal", "", "untagged")
		s.v("TAGGED", tagged.ID)
		s.v("BARE", bare.ID)
		s.v("BARE_SLUG", bare.Slug)
		s.v("LITERAL", literal.ID)
	})
}

// The four sets from the ordering rule, created in a deliberately wrong
// order so the assertion cannot pass by accident of creation time.
// Playwright kept to the groups holding #ord- since its server had other
// tagged rows; this server has only these, so all the labels are checked.
func TestE2ETagFilterGroupOrder(t *testing.T) {
	tagFilterTest(t, useful(0), `tag groups are ordered by first tag, then second. Four conversations are tagged ord-b and ord-d; ord-b; ord-a and ord-b; and ord-b and ord-c; there are no others. One of them has slug {{B_SLUG}}. `+tagFilterDrawer+drawerDirect+drawerSteps+`1. Navigate to /c/{{B_SLUG}} and wait up to 30s for data-testid "message-input" to be visible. 2. Open the drawer. 3. Click the button "View options", then the button "Tags". 4. The texts of ".conversation-group .conversation-group-label" are exactly ["#ord-a #ord-b", "#ord-b", "#ord-b #ord-c", "#ord-b #ord-d"].`, func(s *shelley) {
		s.tagFilterConv("order tags bd", "", "ord-b", "ord-d")
		b := s.tagFilterConv("order tags b", "", "ord-b")
		s.tagFilterConv("order tags ab", "", "ord-a", "ord-b")
		s.tagFilterConv("order tags bc", "", "ord-b", "ord-c")
		s.v("B_SLUG", b.Slug)
	})
}

func TestE2ETagFilterGroupByTagSets(t *testing.T) {
	tagFilterTest(t, useful(0), `group by tag buckets whole tag sets, with untagged last. Conversations {{BOTH}} (slug {{BOTH_SLUG}}) is tagged gt-red and gt-blue, {{BOTH_AGAIN}} gt-blue and gt-red (the same set, in the opposite order), {{SINGLE}} gt-red, and {{NONE}} nothing. `+tagFilterDrawer+drawerQuery+drawerDirect+drawerSteps+`"The group of X" is ".conversation-group:has(.conversation-item[data-conversation-id='X'])" and its label is the ".conversation-group-label" inside it. 1. Navigate to /c/{{BOTH_SLUG}} and wait up to 30s for data-testid "message-input" to be visible. 2. Open the drawer. 3. Click the button "View options", then the button "Tags". 4. There is exactly 1 group of {{BOTH}} (assert_count). 5. Its label's text is exactly "#gt-blue #gt-red" (the sorted set), and its title attribute is exactly "#gt-blue #gt-red" (the full readable set the narrow drawer truncates). 6. The group of {{BOTH}} holds exactly 1 [data-conversation-id="{{BOTH_AGAIN}}"] (one group holds both). 7. The label of the group of {{SINGLE}} reads exactly "#gt-red" (a subset is its own group), and the group of {{SINGLE}} holds no [data-conversation-id="{{BOTH}}"]. 8. The label of the group of {{NONE}} reads exactly "Untagged". 9. The page holds exactly 1 [data-conversation-id="{{BOTH}}"] (every conversation appears exactly once). 10. Open the search. 11. Set the query to "tag:gt-blue " (grouping and filtering compose). 12. The row of {{BOTH}} is visible; the rows of {{SINGLE}} and {{NONE}} are not visible (assert_not_visible). 13. The label of the group of {{BOTH}} still reads exactly "#gt-blue #gt-red".`, func(s *shelley) {
		both := s.tagFilterConv("group tags both", "", "gt-red", "gt-blue")
		again := s.tagFilterConv("group tags both again", "", "gt-blue", "gt-red")
		single := s.tagFilterConv("group tags single", "", "gt-red")
		none := s.tagFilterConv("group tags none", "")
		s.v("BOTH", both.ID)
		s.v("BOTH_SLUG", both.Slug)
		s.v("BOTH_AGAIN", again.ID)
		s.v("SINGLE", single.ID)
		s.v("NONE", none.ID)
	})
}

// Rows inside a group keep their own stable order: a newcomer is prepended
// even when a plain sort would place it last (retagging does not bump
// updated_at), and "Re-sort now" restores the plain order.
func TestE2ETagFilterGroupJoinOrder(t *testing.T) {
	tagFilterTest(t, useful(0), `a row that joins a group lands on top of it until re-sorted. Three conversations sort {{FIRST}}, {{SECOND}}, {{MOVER}} in the drawer's plain order (newest bucket of updated_at first, then newest created). {{FIRST}} and {{SECOND}} are tagged so-home; {{MOVER}} (slug {{MOVER_SLUG}}) is tagged so-away. Call "join-home" retags {{MOVER}} so-home (which does not change its updated_at). `+tagFilterDrawer+drawerDirect+drawerSteps+`"The home ids" are the data-conversation-ids of the rows of the home group in order: what this eval returns: JSON.stringify([...document.querySelectorAll(".conversation-group:has(.conversation-item[data-conversation-id='{{FIRST}}']) .conversation-item")].map(r => r.getAttribute("data-conversation-id"))). 1. Navigate to /c/{{MOVER_SLUG}} and wait up to 30s for data-testid "message-input" to be visible. 2. Open the drawer. 3. Click the button "View options", then the button "Tags". 4. The home ids are exactly ["{{FIRST}}","{{SECOND}}"] (polled with expect). 5. Call "join-home". 6. The home ids are exactly ["{{MOVER}}","{{FIRST}}","{{SECOND}}"] (the newcomer lands on top). 7. Click the button "View options", then the button "Re-sort now". 8. The home ids are exactly ["{{FIRST}}","{{SECOND}}","{{MOVER}}"] (the plain order).`, func(s *shelley) {
		var created []convo
		for _, n := range []string{"one", "two", "three"} {
			created = append(created, s.conversation("group order "+n, ""))
		}
		type conv struct {
			ID      string    `json:"conversation_id"`
			Created time.Time `json:"created_at"`
			Updated time.Time `json:"updated_at"`
		}
		var convs []conv
		for _, c := range created {
			var got struct {
				Conversation conv `json:"conversation"`
			}
			s.api("GET", "/api/conversation/"+c.ID, nil, &got)
			convs = append(convs, got.Conversation)
		}
		// sortConversationsByBucket: newest 5-minute bucket of updated_at
		// first, then newest created, then the greater ID.
		bucket := func(t time.Time) int64 { return t.UnixMilli() / (5 * 60 * 1000) }
		slices.SortFunc(convs, func(a, b conv) int {
			if d := bucket(b.Updated) - bucket(a.Updated); d != 0 {
				return int(d)
			}
			if d := b.Created.Compare(a.Created); d != 0 {
				return d
			}
			return strings.Compare(b.ID, a.ID)
		})
		mover := convs[2].ID
		s.tagFilterSetTags(mover, "so-away")
		s.tagFilterSetTags(convs[0].ID, "so-home")
		s.tagFilterSetTags(convs[1].ID, "so-home")
		s.v("FIRST", convs[0].ID)
		s.v("SECOND", convs[1].ID)
		s.v("MOVER", mover)
		for _, c := range created {
			if c.ID == mover {
				s.v("MOVER_SLUG", c.Slug)
			}
		}
		s.call("join-home", func() string {
			s.tagFilterSetTags(mover, "so-home")
			return "retagged"
		})
	})
}

// ac-terminal-work and workbench exist on donors; typing "work" on target
// should offer both (substring match), ranked prefix-first, with Enter
// committing the highlighted offer. spare-tag is never added to target, so
// its offer keeps the dropdown openable at the end of the test.
func TestE2ETagFilterTagEditorDropdown(t *testing.T) {
	tagFilterTest(t, useful(0), `the row tag editor offers a dropdown of matching tags. Conversation {{DONOR}} is tagged ac-terminal-work and workbench, {{DONOR2}} ac-terminal-work and spare-tag, and {{TARGET}} (slug {{TARGET_SLUG}}) nothing. `+tagFilterDrawer+drawerDirect+drawerSteps+`"The input" is the row of {{TARGET}}'s ".conversation-tag-inline-input"; "the menu" is data-testid "tag-editor-menu" and its offers are its [data-testid="tag-editor-option"] elements. Type with the type action (key by key) and press keys with press_key, both on the focused input. 1. Navigate to /c/{{TARGET_SLUG}} and wait up to 30s for data-testid "message-input" to be visible. 2. Open the drawer. 3. Open the actions of the row of {{TARGET}} and click the menu item "Edit tags" in the menu (exact name). 4. The input has focus. 5. The menu is visible and its [data-tag="ac-terminal-work"] is visible (focusing the empty input offers the vocabulary). 6. Type "work". 7. The menu holds exactly 2 offers, whose data-tag attributes are in order "workbench" then "ac-terminal-work" (the prefix hit ranks first): eval with expect '["workbench","ac-terminal-work"]' of JSON.stringify([...document.querySelectorAll("[data-testid='tag-editor-menu'] [data-testid='tag-editor-option']")].map(o => o.getAttribute("data-tag"))). 8. Press ArrowDown, then Enter (arrow keys move the highlight; Enter commits the highlighted offer). 9. The row of {{TARGET}}'s ".conversation-tag" with text "ac-terminal-work" (has_text) is visible. 10. The input still has focus and its value is "" (the editor stays open, cleared and focused). 11. Type "work". 12. The menu holds exactly 1 offer, with data-tag "workbench" (the tag on the row is excluded): the same eval with expect '["workbench"]'. 13. Click the first offer. 14. The row of {{TARGET}}'s ".conversation-tag" with text "workbench" is visible. 15. Type "brand-new-tag". 16. There is no menu (text matching nothing closes it). 17. Press Enter. 18. The row of {{TARGET}}'s ".conversation-tag" with text "brand-new-tag" is visible (Enter saved it literally). 19. The menu is visible (spare-tag is still on offer). 20. Press Escape. 21. There is no menu, and the input still has focus (Escape peels one layer). 22. Press Escape. 23. The row of {{TARGET}} holds no ".conversation-tag-inline-input" (the editor closed).`, func(s *shelley) {
		donor := s.tagFilterConv("autocomplete donor", "", "ac-terminal-work", "workbench")
		donor2 := s.tagFilterConv("autocomplete donor 2", "", "ac-terminal-work", "spare-tag")
		target := s.conversation("autocomplete target", "")
		s.v("DONOR", donor.ID)
		s.v("DONOR2", donor2.ID)
		s.v("TARGET", target.ID)
		s.v("TARGET_SLUG", target.Slug)
	})
}

// A tag longer than the chip's max-width used to push the remove button
// into the chip's hidden overflow, where it could not be clicked.
func TestE2ETagFilterOverlongTagRemovable(t *testing.T) {
	tagFilterTest(t, useful(0), `the remove button on an overlong tag stays clickable. Conversation {{ID}} (slug {{SLUG}}) is tagged {{LONG}} ("overlong-" and 120 x's). `+tagFilterDrawer+drawerDirect+drawerSteps+`1. Navigate to /c/{{SLUG}} and wait up to 30s for data-testid "message-input" to be visible. 2. Open the drawer. 3. Open the actions of the row of {{ID}} and click the menu item "Edit tags" in the menu (exact name). 4. Click the row of {{ID}}'s button[aria-label="Remove tag {{LONG}}"] (a real click, which needs the button visible and not covered). 5. The row of {{ID}} holds no ".conversation-tag-removable".`, func(s *shelley) {
		long := "overlong-" + strings.Repeat("x", 120)
		c := s.tagFilterConv("overlong tag removal", "", long)
		s.v("ID", c.ID)
		s.v("SLUG", c.Slug)
		s.v("LONG", long)
	})
}
