package test

import (
	"context"
	"net/http"
	"net/http/httptest"
	"os"
	"testing"

	"github.com/boldsoftware/shelley/lazycue"
)

// The fake clock behaves as Playwright's page.clock does.
func TestE2EClock(t *testing.T) {
	if os.Getenv("LAZYCUE_INTEGRATION") == "" {
		t.Skip("set LAZYCUE_INTEGRATION=1 to run the LazyCue browser integration tests")
	}
	t.Parallel()
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte(`<!doctype html><title>clock</title>`))
	}))
	defer ts.Close()
	br, err := lazycue.NewBrowser(context.Background(), lazycue.Launch{InitScript: "window.__CLOCK_START__ = 1700000000000;\n" + clockJS})
	if err != nil {
		t.Fatal(err)
	}
	defer br.Close()
	eval := func(expr, want string) lazycue.Step {
		return lazycue.Step{Action: lazycue.ActionEval, Expression: expr, Expect: &want, Timeout: "5s"}
	}
	steps := []lazycue.Step{
		{Action: lazycue.ActionNavigate, URL: "/"},
		// Time flows from the start time, and timers fire as it does.
		eval(`Date.now() >= 1700000000000 && Date.now() < 1700000060000`, "true"),
		eval(`Promise.all(Array.from({length: 200}, (_, i) => new Promise(r => setTimeout(r, i % 7)))).then(() => 'all fired')`, "all fired"),
		// Paused, runFor fires intervals as often as they come due.
		eval(`(async () => {
			await __clock.pauseAt(1700000100000);
			const t0 = Date.now(), log = [];
			const id = setInterval(() => log.push(Date.now() - t0), 1000);
			await __clock.runFor(3500);
			clearInterval(id);
			return t0 + ' ' + log.join(',');
		})()`, "1700000100000 1000,2000,3000"),
		// fastForward fires a late timer once, at the time jumped to.
		eval(`(async () => {
			const t0 = Date.now(), log = [];
			const id = setInterval(() => log.push(Date.now() - t0), 1000);
			await __clock.fastForward(5000);
			await __clock.runFor(1000);
			clearInterval(id);
			return log.join(',');
		})()`, "5000,6000"),
		// A timer set from a timer's callback is a millisecond later, so a
		// self-rescheduling one cannot stall runFor.
		eval(`(async () => {
			let n = 0, stop = false;
			const tick = () => { n++; if (!stop) setTimeout(tick, 0); };
			setTimeout(tick, 0);
			await __clock.runFor(10);
			stop = true;
			return n;
		})()`, "11"),
		// The page runs a task after each timer, so awaited sleeps advance.
		eval(`(async () => {
			let n = 0;
			(async () => { while (n < 100) { await new Promise(r => setTimeout(r, 100)); n++; } })();
			await __clock.runFor(1050);
			return n;
		})()`, "10"),
		// Time stays put while paused, and flows again after resume.
		eval(`(async () => {
			const t0 = Date.now(), p0 = performance.now();
			let fired = false;
			setTimeout(() => fired = true, 0);
			await fetch('/');
			return [Date.now() - t0, performance.now() - p0, fired].join(' ');
		})()`, "0 0 false"),
		eval(`(async () => {
			__clock.resume();
			const t0 = Date.now();
			await new Promise(r => setTimeout(r, 20));
			return Date.now() - t0 >= 20;
		})()`, "true"),
		// Pausing while flowing time is still firing timers waits for them.
		eval(`(async () => {
			let n = 0;
			for (let i = 0; i < 50; i++) setTimeout(() => n++, 0);
			await fetch('/');
			const t = Date.now() + 1000;
			await __clock.pauseAt(t);
			return [n, Date.now() === t].join(' ');
		})()`, "50 true"),
		eval(`__clock.setFixedTime(1600000000000); [Date.now(), new Date().getTime()].join(' ')`, "1600000000000 1600000000000"),
	}
	results, err := br.ExecuteSteps(context.Background(), &lazycue.Env{BaseURL: ts.URL}, steps)
	for _, r := range results {
		if !r.Pass {
			t.Errorf("%s: %s", r.Summary, r.Error)
		}
	}
	if err != nil {
		t.Fatal(err)
	}
}
