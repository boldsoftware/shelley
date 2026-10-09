import { test, expect } from '@playwright/test';
import { gunzipSync } from 'zlib';
import { readFileSync, readdirSync } from 'fs';
import { fileURLToPath } from 'url';
import { dirname, join } from 'path';
import { createConversationViaAPI } from './helpers';

// The whole e2e suite runs with reducedMotion: 'reduce' (see playwright.config.ts),
// which is what keeps modal open/close fast. These tests protect the two halves
// of that bargain: motion really is suppressed, and the things that must keep
// moving still do.

const uiDir = join(dirname(fileURLToPath(import.meta.url)), '..');

// Every stylesheet the server actually ships, decompressed. Reading dist rather
// than src means dependency CSS is covered too -- PrimeIcons, for one, ships its
// own reduced-motion rules.
function readServedStylesheets(): string[] {
  const distDir = join(uiDir, 'dist');
  const names = readdirSync(distDir).filter((n) => n.endsWith('.css.gz'));
  if (names.length === 0) {
    throw new Error(`no .css.gz in ${distDir} -- run \`pnpm run build\` first`);
  }
  return names.map((n) => gunzipSync(readFileSync(join(distDir, n))).toString('utf8'));
}

test.describe('reduced motion', () => {
  test('mobile Stop pulse only animates when motion is allowed', async ({ page }) => {
    await page.setViewportSize({ width: 390, height: 844 });
    await page.setContent(
      '<meta name="viewport" content="width=device-width, initial-scale=1">' +
        '<div class="message-controls-status-slot"><button class="status-stop-button">Stop</button></div>',
    );
    await page.addStyleTag({ content: readServedStylesheets().join('\n') });
    const animationName = () =>
      page.locator('.status-stop-button').evaluate((el) => getComputedStyle(el).animationName);

    await page.emulateMedia({ reducedMotion: 'reduce' });
    await expect.poll(animationName).toBe('none');
    await page.emulateMedia({ reducedMotion: 'no-preference' });
    await expect.poll(animationName).toBe('mobile-stop-working-pulse');
    await page.emulateMedia({ reducedMotion: 'reduce' });
    await expect.poll(animationName).toBe('none');
  });

  test('every infinite animation still animates under reduce', async ({ page }) => {
    // Ask the browser rather than parsing CSS ourselves. An earlier regex
    // version of this test had two silent false negatives (it anchored on the
    // wrong @media block, and it missed `/* comment */ .spinner {` because the
    // comment was part of the selector text), and hand-rolled CSS parsing has
    // no shortage of other holes: nested rules, the
    // animation-iteration-count longhand, ::before, non-class selectors.
    // Loading the real stylesheets and reading computed styles has none of them.
    //
    // Scans everything the app actually serves, not just our own styles.css:
    // dependency CSS ships reduce rules too, and PrimeIcons' freezes .pi-spin by
    // setting animation-iteration-count to 1. Nothing in our source uses that
    // class today, so this currently only guards against someone reaching for it.
    const cssTexts = readServedStylesheets();

    // Collect every rule that animates forever, via either the shorthand or the
    // longhand, using the browser's own CSSOM -- including rules nested in
    // @media/@supports. Skip the reduce block itself: that is the thing under
    // test, and its restore rule is not what makes something animate.
    // Inject via textContent, not setContent with an interpolated <style>:
    // going through the HTML parser escapes the CSS and yields zero rules.
    await page.setContent('<div></div>');
    const selectors: string[] = await page.evaluate((texts) => {
      const sheets = texts.map((cssText) => {
        const styleEl = document.createElement('style');
        styleEl.textContent = cssText;
        document.head.appendChild(styleEl);
        return styleEl.sheet!;
      });
      const out: string[] = [];
      const visit = (rules: CSSRuleList) => {
        for (const rule of Array.from(rules)) {
          // @keyframes also exposes .cssRules (its stops), so dispatch on the
          // rule type rather than on the presence of children.
          if (rule instanceof CSSKeyframesRule) continue;
          if (rule instanceof CSSGroupingRule) {
            const media = (rule as CSSMediaRule).conditionText ?? '';
            // Skip the reduce block: that is the thing under test.
            if (media.includes('prefers-reduced-motion')) continue;
            visit(rule.cssRules);
            continue;
          }
          if (!(rule instanceof CSSStyleRule)) continue;
          // A rule can declare several animations, in which case the CSSOM
          // reports e.g. "infinite, 1" -- any infinite component means part of
          // this rule loops forever. Exact equality would miss those.
          const counts = (rule.style.animationIterationCount || '')
            .split(',')
            .map((c) => c.trim());
          if (!counts.includes('infinite')) continue;
          // selectorText can be a comma-separated list. Emit each selector
          // separately: testing only one of them would let a frozen sibling
          // through (`.a, .b` where only .b is exempted).
          for (const sel of rule.selectorText.split(',')) {
            const trimmed = sel.trim();
            if (trimmed) out.push(trimmed);
          }
        }
      };
      // Use each element's own .sheet: freshly-injected sheets do not show up in
      // document.styleSheets in this context.
      for (const sheet of sheets) visit(sheet.cssRules);
      return [...new Set(out)];
    }, cssTexts);
    expect(selectors.length, 'no infinite animations found -- is the parse working?').toBeGreaterThan(
      0,
    );

    // Now the actual question: with reduce in effect, does an element matching
    // each of those selectors still have a real animation duration? Build one
    // element per selector from the selector itself so compound and descendant
    // selectors (and ::before) are handled without interpretation.
    const frozen: string[] = await page.evaluate((sels) => {
      const bad: string[] = [];
      for (const sel of sels) {
        // Turn a selector into markup that matches it: nest a div per
        // descendant part, applying that part's classes to it.
        const parts = sel.split(/\s+/).filter(Boolean);
        let root: HTMLElement | null = null;
        let cur: HTMLElement | null = null;
        let pseudo = '';
        for (const part of parts) {
          // Honour an element type if the selector names one (".thinking-dots
          // span" needs a real <span>, or the rule will not match and the test
          // reports a false freeze).
          const tag = part.match(/^[a-zA-Z][a-zA-Z0-9]*/);
          const el = document.createElement(tag ? tag[0] : 'div');
          const classes = part.match(/\.[A-Za-z0-9_-]+/g) ?? [];
          for (const c of classes) el.classList.add(c.slice(1));
          const p = part.match(/::[a-z-]+$/);
          if (p) pseudo = p[0];
          if (!root) root = el;
          if (cur) cur.appendChild(el);
          cur = el;
        }
        if (!root || !cur) continue;
        document.body.appendChild(root);
        const dur = getComputedStyle(cur, pseudo || undefined).animationDuration;
        // Sub-millisecond duration means the reduce override caught it and it
        // will sit frozen on one frame. A finite iteration count is the other
        // way to stop something that should loop: PrimeIcons' own reduce rule
        // freezes .pi-spin that way, leaving the duration untouched, so checking
        // duration alone misses it.
        const iter = getComputedStyle(cur, pseudo || undefined).animationIterationCount;
        if (parseFloat(dur) < 0.1) {
          bad.push(`${sel} (animation-duration: ${dur})`);
        } else if (!iter.split(',').some((c) => c.trim() === 'infinite')) {
          bad.push(`${sel} (animation-iteration-count: ${iter})`);
        }
        root.remove();
      }
      return bad;
    }, selectors);

    expect(
      frozen,
      'These selectors animate forever but are frozen by the reduced-motion override in ' +
        'styles.css, so they will sit on a single frame and make a working UI look hung. ' +
        'Add them to the restore list in the reduce block.',
    ).toEqual([]);
  });

  test('modal open/close is not animated, and spinners still spin', async ({ page, request }) => {
    // Any conversation will do; the Feature flags modal (opened from the
    // command palette) is the cheapest consumer of the shared Modal.
    const slug = await createConversationViaAPI(request, 'echo: reduced motion');
    await page.goto(`/c/${slug}`);
    await page.waitForLoadState('domcontentloaded');

    // The emulation must actually be in effect. Playwright's top-level
    // `reducedMotion` option silently fails to reach the page fixture (1.60),
    // so this guards the contextOptions workaround in playwright.config.ts:
    // without it the CSS below is dead and the suite silently goes slow again.
    expect(
      await page.evaluate(() => matchMedia('(prefers-reduced-motion: reduce)').matches),
      'prefers-reduced-motion is not being emulated; check contextOptions in playwright.config.ts',
    ).toBe(true);

    await page.keyboard.press('ControlOrMeta+k');
    const search = page.locator('.command-palette-input');
    await expect(search).toBeVisible();
    await search.fill('feature');
    await page.locator('.command-palette-item').first().click();
    await expect(page.locator('.modal-title')).toHaveText('Feature flags');

    // The dialog and its mask must not be running a 0.3s entrance: that is the
    // animation Playwright waits out on every click.
    const durations = await page.evaluate(() =>
      [...document.querySelectorAll('.p-dialog, .p-dialog-mask')].map(
        (e) => getComputedStyle(e as HTMLElement).animationDuration,
      ),
    );
    expect(durations.length).toBeGreaterThan(0);
    for (const d of durations) expect(parseFloat(d)).toBeLessThan(0.05);

    // Close it too, and check the *leave* animation the same way. The enter and
    // leave classes are separate rules in PrimeVue's theme, and Playwright pays
    // for both, so asserting only on enter would let a regression in
    // .p-dialog-leave-active through. Sample while the leave is in flight.
    const leaveDurations = await page.evaluate(async () => {
      const closeBtn = document.querySelector(
        '.modal-header .btn-icon',
      ) as HTMLElement | null;
      closeBtn?.click();
      await new Promise((r) => requestAnimationFrame(() => r(null)));
      return [...document.querySelectorAll('.p-dialog, .p-dialog-mask')].map((e) => ({
        cls: (e as HTMLElement).className,
        dur: getComputedStyle(e as HTMLElement).animationDuration,
      }));
    });
    for (const { cls, dur } of leaveDurations) {
      expect(parseFloat(dur), `leaving element still animates: ${cls}`).toBeLessThan(0.05);
    }
    await expect(page.locator('.modal')).toHaveCount(0);

    // A spinner injected with a known exempt class must still animate.
    const spinnerDuration = await page.evaluate(() => {
      const el = document.createElement('div');
      el.className = 'spinner';
      document.body.appendChild(el);
      const d = getComputedStyle(el).animationDuration;
      el.remove();
      return d;
    });
    expect(parseFloat(spinnerDuration)).toBeGreaterThan(0.1);
  });
});
