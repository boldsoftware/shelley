//go:build race

package lazycue

// raceEnabled is set when tests run with -race, which slows chromedp's
// message handling several times over.
const raceEnabled = true
