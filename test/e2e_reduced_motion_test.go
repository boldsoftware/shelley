package test

import (
	"compress/gzip"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// Ported from ui/e2e/reduced-motion.spec.ts. The whole suite runs with
// prefers-reduced-motion: reduce, which is what keeps modal open and close
// fast. These tests protect both halves of that bargain: motion really is
// suppressed, and the things that must keep moving still do.

// reducedMotionSheets serves every stylesheet the server ships, decompressed
// from ui/dist, at /reduced-motion/<name>.css, and returns their links.
// Reading dist rather than src covers dependency CSS too: PrimeIcons, for
// one, ships its own reduced-motion rules.
func (s *shelley) reducedMotionSheets() string {
	dist := filepath.Join(e2eModuleDir, "ui", "dist")
	names, err := filepath.Glob(filepath.Join(dist, "*.css.gz"))
	s.check(err)
	if len(names) == 0 {
		s.fail("no .css.gz in %s: build the UI first", dist)
	}
	sheets := map[string][]byte{}
	var links strings.Builder
	for _, name := range names {
		f, err := os.Open(name)
		s.check(err)
		zr, err := gzip.NewReader(f)
		s.check(err)
		css, err := io.ReadAll(zr)
		f.Close()
		s.check(err)
		base := strings.TrimSuffix(filepath.Base(name), ".gz")
		sheets[base] = css
		fmt.Fprintf(&links, `<link rel="stylesheet" href="/reduced-motion/%s">`, base)
	}
	s.route("GET /reduced-motion/{name}", func(w http.ResponseWriter, r *http.Request) {
		css, ok := sheets[r.PathValue("name")]
		if !ok {
			http.NotFound(w, r)
			return
		}
		w.Header().Set("Content-Type", "text/css")
		w.Write(css)
	})
	return links.String()
}

// reducedMotionPage serves html at path with the shipped stylesheets.
func (s *shelley) reducedMotionPage(path, html string) {
	page := `<!doctype html><meta name="viewport" content="width=device-width, initial-scale=1">` + s.reducedMotionSheets() + html
	s.route("GET "+path, func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		io.WriteString(w, page)
	})
}

// reducedMotionLoaded is the description's check that every shipped
// stylesheet loaded, without which the computed styles prove nothing.
const reducedMotionLoaded = `Eval [...document.querySelectorAll('link[rel="stylesheet"]')].every((l) => l.sheet && l.sheet.cssRules.length > 0): it must return true (allow 15s): every shipped stylesheet loaded.`

func TestE2EReducedMotionStopStaysStatic(t *testing.T) {
	e2e(t, `The mobile Stop button stays static whatever the motion preference. The page /reduced-motion/stop is a phone-width page holding a ".status-stop-button" button in a ".message-controls-status-slot", with every stylesheet the server ships. "Its animation" is getComputedStyle(document.querySelector('.status-stop-button')).animationName. 1. Navigate to /reduced-motion/stop. 2. `+reducedMotionLoaded+` 3. Emulate the media feature prefers-reduced-motion: reduce. 4. Its animation should be exactly "none" (eval with expect, which polls). 5. Emulate prefers-reduced-motion: no-preference. 6. Its animation should be exactly "none". 7. Emulate prefers-reduced-motion: reduce. 8. Its animation should be exactly "none".`, func(s *shelley) {
		s.viewport(390, 844)
		s.reducedMotionPage("/reduced-motion/stop", `<div class="message-controls-status-slot"><button class="status-stop-button">Stop</button></div>`)
	})
}

// reducedMotionScan finds every selector the shipped stylesheets animate
// forever, and those of them the reduced-motion override freezes. It asks
// the browser rather than parsing CSS: an earlier regex version of the spec
// had two silent false negatives (it anchored on the wrong @media block, and
// missed "/* comment */ .spinner {" because the comment was part of the
// selector), and hand-rolled parsing has no shortage of other holes: nested
// rules, the animation-iteration-count longhand, ::before, non-class
// selectors.
const reducedMotionScan = `<script>
// Every rule that animates forever, by the shorthand or the longhand, from the
// browser's own CSSOM, including rules nested in @media and @supports. The
// reduce block itself is skipped: it is what is under test, and its restore
// rule is not what makes something animate.
window.__infiniteSelectors = () => {
  const out = [];
  const visit = (rules) => {
    for (const rule of Array.from(rules)) {
      // @keyframes also has cssRules (its stops): dispatch on the rule type.
      if (rule instanceof CSSKeyframesRule) continue;
      if (rule instanceof CSSGroupingRule) {
        if ((rule.conditionText ?? '').includes('prefers-reduced-motion')) continue;
        visit(rule.cssRules);
        continue;
      }
      if (!(rule instanceof CSSStyleRule)) continue;
      // A rule declaring several animations reports e.g. "infinite, 1".
      const counts = (rule.style.animationIterationCount || '').split(',').map((c) => c.trim());
      if (!counts.includes('infinite')) continue;
      // Each selector of a list on its own: testing one would let a frozen
      // sibling through.
      for (const sel of rule.selectorText.split(',')) {
        if (sel.trim()) out.push(sel.trim());
      }
    }
  };
  for (const link of document.querySelectorAll('link[rel="stylesheet"]')) visit(link.sheet.cssRules);
  return [...new Set(out)];
};
// The selectors whose element, built from the selector itself (a div, or the
// element the part names, per descendant part, with its classes), has a
// sub-0.1s animation duration or a finite iteration count: the reduce
// override froze it on one frame. PrimeIcons' own reduce rule freezes .pi-spin
// by its iteration count, leaving the duration alone.
window.__frozen = (sels) => {
  const bad = [];
  for (const sel of sels) {
    let root = null;
    let cur = null;
    let pseudo = '';
    for (const part of sel.split(/\s+/).filter(Boolean)) {
      const tag = part.match(/^[a-zA-Z][a-zA-Z0-9]*/);
      const el = document.createElement(tag ? tag[0] : 'div');
      for (const c of part.match(/\.[A-Za-z0-9_-]+/g) ?? []) el.classList.add(c.slice(1));
      const p = part.match(/::[a-z-]+$/);
      if (p) pseudo = p[0];
      if (!root) root = el;
      if (cur) cur.appendChild(el);
      cur = el;
    }
    if (!root) continue;
    document.body.appendChild(root);
    const style = getComputedStyle(cur, pseudo || undefined);
    const dur = style.animationDuration;
    const iter = style.animationIterationCount;
    if (parseFloat(dur) < 0.1) bad.push(sel + ' (animation-duration: ' + dur + ')');
    else if (!iter.split(',').some((c) => c.trim() === 'infinite')) bad.push(sel + ' (animation-iteration-count: ' + iter + ')');
    root.remove();
  }
  return bad;
};
</script>`

func TestE2EReducedMotionInfiniteAnimationsStillAnimate(t *testing.T) {
	// Selectors that animate forever but are frozen by the reduced-motion
	// override in styles.css sit on a single frame and make a working UI
	// look hung: they belong in the reduce block's restore list.
	e2e(t, `Every infinite animation still animates under reduced motion. The page /reduced-motion/scan has every stylesheet the server ships, and two helpers: window.__infiniteSelectors() lists the selectors of every rule outside a prefers-reduced-motion block that animates forever, and window.__frozen(selectors) lists those of the given selectors whose element the reduce override has frozen. Motion is reduced (prefers-reduced-motion: reduce). 1. Navigate to /reduced-motion/scan. 2. `+reducedMotionLoaded+` 3. Eval window.__infiniteSelectors().length > 0: it must return true (no infinite animations would mean the scan is broken). 4. Eval JSON.stringify(window.__frozen(window.__infiniteSelectors())): it must return exactly "[]".`, func(s *shelley) {
		s.reducedMotionPage("/reduced-motion/scan", `<div></div>`+reducedMotionScan)
	})
}

func TestE2EReducedMotionModalAndSpinners(t *testing.T) {
	// The Feature flags modal, from the command palette, is the cheapest
	// user of the shared Modal. Its 0.3s entrance and exit animations are
	// what a test would wait out on every click.
	e2e(t, `Modal open and close are not animated under reduced motion, and spinners still spin. The conversation /c/{{SLUG}} was started with "echo: reduced motion". "The dialog durations" are [...document.querySelectorAll('.p-dialog, .p-dialog-mask')].map((e) => parseFloat(getComputedStyle(e).animationDuration)). 1. Navigate to /c/{{SLUG}}. 2. Eval matchMedia('(prefers-reduced-motion: reduce)').matches: it must return true (the emulation is in effect). 3. Press "k" with the Control modifier. 4. ".command-palette-input" should be visible. 5. Fill it with "feature". 6. Click the first ".command-palette-item". 7. ".modal-title" should read exactly "Feature flags". 8. The entrance would last only 0.3s, so sample it at once rather than polling: in one eval WITHOUT "expect" (so that it runs once), record window.__enter = <the dialog durations>. Then eval window.__enter.length > 0 && window.__enter.every((x) => x < 0.05): it must return true. 9. ".modal" should be visible. 10. In one eval WITHOUT "expect" (so that it runs once), sample the leave animation while it is in flight: (async () => { document.querySelector('.modal-header .btn-icon').click(); await new Promise((r) => requestAnimationFrame(r)); window.__leave = [...document.querySelectorAll('.p-dialog, .p-dialog-mask')].map((e) => e.className + ': ' + getComputedStyle(e).animationDuration); return window.__leave; })(). 11. Eval window.__leave.every((s) => parseFloat(s.slice(s.lastIndexOf(': ') + 2)) < 0.05): it must return true (no leaving element still animates; the list may be empty if the dialog is already gone). 12. There must be exactly 0 ".modal" elements. 13. Eval (() => { const el = document.createElement('div'); el.className = 'spinner'; document.body.appendChild(el); const d = getComputedStyle(el).animationDuration; el.remove(); return parseFloat(d) > 0.1; })(): it must return true (a spinner still spins).`, func(s *shelley) {
		s.v("SLUG", s.conversation("echo: reduced motion", "").Slug)
	})
}
