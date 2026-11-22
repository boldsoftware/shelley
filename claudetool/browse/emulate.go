package browse

import (
	"context"
	"encoding/json"
	"fmt"
	"sort"
	"strings"

	"github.com/chromedp/cdproto/emulation"
	"github.com/chromedp/cdproto/page"
	"github.com/chromedp/cdproto/runtime"
	"github.com/chromedp/chromedp"
	"shelley.exe.dev/llm"
)

// devicePreset defines the parameters for a known device emulation profile.
type devicePreset struct {
	Width, Height int64
	DPR           float64
	Mobile, Touch bool
	UserAgent     string
}

var devicePresets = map[string]devicePreset{
	"iphone_se": {
		Width: 375, Height: 667, DPR: 2, Mobile: true, Touch: true,
		UserAgent: "Mozilla/5.0 (iPhone; CPU iPhone OS 17_0 like Mac OS X) AppleWebKit/605.1.15 (KHTML, like Gecko) Version/17.0 Mobile/15E148 Safari/604.1",
	},
	"iphone_14": {
		Width: 390, Height: 844, DPR: 3, Mobile: true, Touch: true,
		UserAgent: "Mozilla/5.0 (iPhone; CPU iPhone OS 17_0 like Mac OS X) AppleWebKit/605.1.15 (KHTML, like Gecko) Version/17.0 Mobile/15E148 Safari/604.1",
	},
	"iphone_14_pro_max": {
		Width: 430, Height: 932, DPR: 3, Mobile: true, Touch: true,
		UserAgent: "Mozilla/5.0 (iPhone; CPU iPhone OS 17_0 like Mac OS X) AppleWebKit/605.1.15 (KHTML, like Gecko) Version/17.0 Mobile/15E148 Safari/604.1",
	},
	"ipad": {
		Width: 810, Height: 1080, DPR: 2, Mobile: true, Touch: true,
		UserAgent: "Mozilla/5.0 (iPad; CPU OS 17_0 like Mac OS X) AppleWebKit/605.1.15 (KHTML, like Gecko) Version/17.0 Mobile/15E148 Safari/604.1",
	},
	"ipad_pro": {
		Width: 1024, Height: 1366, DPR: 2, Mobile: true, Touch: true,
		UserAgent: "Mozilla/5.0 (iPad; CPU OS 17_0 like Mac OS X) AppleWebKit/605.1.15 (KHTML, like Gecko) Version/17.0 Mobile/15E148 Safari/604.1",
	},
	"pixel_7": {
		Width: 412, Height: 915, DPR: 2.625, Mobile: true, Touch: true,
		UserAgent: "Mozilla/5.0 (Linux; Android 14; Pixel 7) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/120.0.0.0 Mobile Safari/537.36",
	},
	"galaxy_s23": {
		Width: 360, Height: 780, DPR: 3, Mobile: true, Touch: true,
		UserAgent: "Mozilla/5.0 (Linux; Android 14; SM-S911B) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/120.0.0.0 Mobile Safari/537.36",
	},
	"desktop_hd": {
		Width: 1920, Height: 1080, DPR: 1, Mobile: false, Touch: false,
	},
	"desktop_4k": {
		Width: 3840, Height: 2160, DPR: 2, Mobile: false, Touch: false,
	},
}

// emulateInput carries parameters for the emulate_* browser actions. It is
// populated by runCombined from the combined browser tool input.
type emulateInput struct {
	Device            string
	Width             int64
	Height            int64
	DeviceScaleFactor float64
	Mobile            bool
	Touch             bool
	ColorScheme       string
	Media             string
	Timeout           string
}

func (b *BrowseTools) emulateHelp() llm.ToolOut {
	var sb strings.Builder
	sb.WriteString("Device Emulation — actions on the browser tool.\n")
	sb.WriteString("=======================================\n\n")
	sb.WriteString("Actions (pass as the browser tool's \"action\"):\n")
	sb.WriteString("  emulate_help         - Show this help message\n")
	sb.WriteString("  emulate_device       - Emulate a preset device (param: device)\n")
	sb.WriteString("  emulate_custom       - Custom viewport emulation (params: width, height, device_scale_factor, mobile, touch); clears any prior UA override\n")
	sb.WriteString("  emulate_reset        - Reset to default viewport (1280x720); color scheme and media type are unchanged\n")
	sb.WriteString("  emulate_color_scheme - Set prefers-color-scheme (param: color_scheme: light, dark, or system to clear); persists across navigations until changed\n")
	sb.WriteString("  emulate_media        - Emulate CSS media type (param: media: 'print', 'screen', or empty to clear)\n")
	sb.WriteString("\nAvailable device presets:\n")
	names := make([]string, 0, len(devicePresets))
	for name := range devicePresets {
		names = append(names, name)
	}
	sort.Strings(names)
	for _, name := range names {
		preset := devicePresets[name]
		mobileStr := "desktop"
		if preset.Mobile {
			mobileStr = "mobile"
		}
		sb.WriteString(fmt.Sprintf("  %-20s %dx%d @ %.3gx DPR (%s)\n", name, preset.Width, preset.Height, preset.DPR, mobileStr))
	}
	return llm.ToolOut{LLMContent: llm.TextContent(sb.String())}
}

func (b *BrowseTools) emulateDevice(ctx context.Context, input emulateInput) llm.ToolOut {
	if input.Device == "" {
		return llm.ErrorfToolOut("device parameter is required")
	}

	preset, ok := devicePresets[input.Device]
	if !ok {
		var names []string
		for name := range devicePresets {
			names = append(names, name)
		}
		sort.Strings(names)
		return llm.ErrorfToolOut("unknown device %q; available: %s", input.Device, strings.Join(names, ", "))
	}

	return b.applyEmulation(ctx, input.Timeout, preset.Width, preset.Height, preset.DPR, preset.Mobile, preset.Touch, preset.UserAgent)
}

func (b *BrowseTools) emulateCustom(ctx context.Context, input emulateInput) llm.ToolOut {
	if input.Width <= 0 || input.Height <= 0 {
		return llm.ErrorfToolOut("width and height are required and must be positive")
	}
	if input.DeviceScaleFactor <= 0 {
		input.DeviceScaleFactor = 1.0
	}

	// Passing "" clears any prior UA override, which is correct: custom viewports
	// have no user_agent parameter, so there is no supported way to carry a
	// custom UA forward.
	return b.applyEmulation(ctx, input.Timeout, input.Width, input.Height, input.DeviceScaleFactor, input.Mobile, input.Touch, "")
}

func (b *BrowseTools) applyEmulation(ctx context.Context, timeout string, width, height int64, dpr float64, mobile, touch bool, userAgent string) llm.ToolOut {
	err := b.browserRun(ctx, timeout, "", chromedp.ActionFunc(func(ctx context.Context) error {
		if err := emulation.SetDeviceMetricsOverride(width, height, dpr, mobile).Do(ctx); err != nil {
			return fmt.Errorf("set device metrics: %w", err)
		}
		if touch {
			if err := emulation.SetTouchEmulationEnabled(true).WithMaxTouchPoints(5).Do(ctx); err != nil {
				return fmt.Errorf("set touch emulation: %w", err)
			}
		} else {
			if err := emulation.SetTouchEmulationEnabled(false).Do(ctx); err != nil {
				return fmt.Errorf("disable touch emulation: %w", err)
			}
		}
		if err := emulation.SetUserAgentOverride(userAgent).Do(ctx); err != nil {
			return fmt.Errorf("set user agent: %w", err)
		}
		return nil
	}))
	if err != nil {
		return llm.ErrorToolOut(err)
	}

	mobileStr := "desktop"
	if mobile {
		mobileStr = "mobile"
	}
	msg := fmt.Sprintf("Emulation applied: %dx%d @ %.3gx DPR (%s)", width, height, dpr, mobileStr)
	if userAgent != "" {
		msg += fmt.Sprintf(", UA=%s", userAgent)
	}
	return llm.ToolOut{LLMContent: llm.TextContent(msg)}
}

func (b *BrowseTools) emulateReset(ctx context.Context, timeout string) llm.ToolOut {
	err := b.browserRun(ctx, timeout, "",
		chromedp.ActionFunc(func(ctx context.Context) error {
			if err := emulation.ClearDeviceMetricsOverride().Do(ctx); err != nil {
				return fmt.Errorf("clear device metrics: %w", err)
			}
			return nil
		}),
		chromedp.EmulateViewport(1280, 720),
		chromedp.ActionFunc(func(ctx context.Context) error {
			if err := emulation.SetTouchEmulationEnabled(false).Do(ctx); err != nil {
				return fmt.Errorf("disable touch emulation: %w", err)
			}
			if err := emulation.SetUserAgentOverride("").Do(ctx); err != nil {
				return fmt.Errorf("clear user agent: %w", err)
			}
			return nil
		}),
	)
	if err != nil {
		return llm.ErrorToolOut(err)
	}

	return llm.ToolOut{LLMContent: llm.TextContent("Emulation reset to default (1280x720)")}
}

// emulatedMedia is the session's CSS media emulation. Emulation.setEmulatedMedia
// replaces the media type and every media feature at once, so both live here
// and are always sent together. GetBrowserContext applies it to every browser
// it starts, so it survives idle shutdowns and crashes as well as navigation.
type emulatedMedia struct {
	Type        string // "", "screen", or "print"
	ColorScheme string // prefers-color-scheme: "light", "dark", or "" for none
}

func (m emulatedMedia) action() chromedp.Action {
	return chromedp.ActionFunc(func(ctx context.Context) error {
		var features []*emulation.MediaFeature
		if m.ColorScheme != "" {
			features = append(features, &emulation.MediaFeature{Name: "prefers-color-scheme", Value: m.ColorScheme})
		}
		return emulation.SetEmulatedMedia().WithMedia(m.Type).WithFeatures(features).Do(ctx)
	})
}

func (m emulatedMedia) String() string {
	var parts []string
	if m.ColorScheme != "" {
		parts = append(parts, "prefers-color-scheme: "+m.ColorScheme)
	}
	if m.Type != "" {
		parts = append(parts, "media type: "+m.Type)
	}
	if len(parts) == 0 {
		return "none"
	}
	return strings.Join(parts, ", ")
}

// settleMediaJS resolves once the page has rendered a media emulation
// change: two animation frames let matchMedia change listeners run, then the
// CSS transitions they started (a theme fade) finish. It waits at most 2s, and
// only for the top document, not iframes.
const settleMediaJS = `Promise.race([
	new Promise(r => requestAnimationFrame(() => requestAnimationFrame(r)))
		.then(() => Promise.all(document.getAnimations().filter(a => a instanceof CSSTransition).map(a => a.finished.catch(() => {})))),
	new Promise(r => setTimeout(r, 2000)),
]).then(() => true)`

// colorSchemeJS is the prefers-color-scheme the page matches.
const colorSchemeJS = `matchMedia("(prefers-color-scheme: dark)").matches ? "dark" : "light"`

// isolatedEval returns an action that evaluates js in the top frame, awaiting
// its promise, and stores the result in res. It runs in an isolated world,
// whose matchMedia, requestAnimationFrame and setTimeout page scripts can't
// replace. chromedp then takes that world as the frame's script context, which
// only DOM queries use; page scripts run in the main world (chromedp.Evaluate),
// but chromedp.Poll would not.
func isolatedEval(js string, res any) chromedp.Action {
	return chromedp.ActionFunc(func(ctx context.Context) error {
		tree, err := page.GetFrameTree().Do(ctx)
		if err != nil {
			return err
		}
		world, err := page.CreateIsolatedWorld(tree.Frame.ID).WithWorldName("shelley-media").Do(ctx)
		if err != nil {
			return err
		}
		v, exc, err := runtime.Evaluate(js).WithContextID(world).WithAwaitPromise(true).WithReturnByValue(true).Do(ctx)
		if err != nil {
			return err
		}
		if exc != nil {
			return exc
		}
		return json.Unmarshal(v.Value, res)
	})
}

// saveMedia makes m the session's media emulation. Nothing is sent yet: the
// next browserRun sends it (see syncMedia), and a browser that starts gets it
// at startup. Callers hold mediaMu, which serializes changes.
func (b *BrowseTools) saveMedia(m emulatedMedia) {
	b.mux.Lock()
	b.media = m
	b.mediaGen++
	b.mux.Unlock()
}

// syncMedia returns an action that sends the session's media emulation to the
// page unless Chrome has confirmed it. browserRun runs it first, so a change
// whose call failed, which Chrome may or may not have, is in place before the
// next action. One send runs at a time, so the last one Chrome gets is the
// newest. A page frozen before Chrome confirms a change blocks every action,
// even a navigate to another site, until the page recovers or the browser
// restarts.
func (b *BrowseTools) syncMedia() chromedp.Action {
	return chromedp.ActionFunc(func(ctx context.Context) error {
		b.mux.Lock()
		synced := b.mediaSent == b.mediaGen
		b.mux.Unlock()
		if synced {
			return nil
		}
		select {
		case b.mediaSync <- struct{}{}:
			defer func() { <-b.mediaSync }()
		case <-ctx.Done():
			return ctx.Err()
		}
		b.mux.Lock()
		m, gen := b.media, b.mediaGen
		synced = b.mediaSent == gen
		b.mux.Unlock()
		if synced {
			return nil
		}
		if err := m.action().Do(ctx); err != nil {
			return fmt.Errorf("sending media emulation (%s): %w", m, err)
		}
		b.mux.Lock()
		b.mediaSent = gen
		b.mux.Unlock()
		return nil
	})
}

// settleMedia returns an action that waits for the page to render the media
// emulation Chrome has, unless it already has. Run it after syncMedia, before
// looking at the page.
func (b *BrowseTools) settleMedia() chromedp.Action {
	return chromedp.ActionFunc(func(ctx context.Context) error {
		b.mux.Lock()
		m, sent, shown := b.media, b.mediaSent, b.mediaShown
		b.mux.Unlock()
		if shown >= sent {
			return nil
		}
		var settled bool
		if err := isolatedEval(settleMediaJS, &settled).Do(ctx); err != nil {
			return fmt.Errorf("waiting for the page to render media emulation (%s): %w", m, err)
		}
		b.mux.Lock()
		b.mediaShown = max(b.mediaShown, sent)
		b.mux.Unlock()
		return nil
	})
}

// parseColorScheme maps a color_scheme input to the prefers-color-scheme value
// to emulate; "system" is "", no override.
func parseColorScheme(s string) (string, error) {
	switch s {
	case "light", "dark":
		return s, nil
	case "system":
		return "", nil
	}
	return "", fmt.Errorf(`color_scheme must be "light", "dark", or "system", got %q`, s)
}

func (b *BrowseTools) emulateColorScheme(ctx context.Context, input emulateInput) llm.ToolOut {
	scheme, err := parseColorScheme(input.ColorScheme)
	if err != nil {
		return llm.ErrorToolOut(err)
	}
	b.mediaMu.Lock()
	defer b.mediaMu.Unlock()
	m := b.media
	m.ColorScheme = scheme
	b.saveMedia(m)
	var got string
	if err := b.browserRun(ctx, input.Timeout, "", b.settleMedia(), isolatedEval(colorSchemeJS, &got)); err != nil {
		return llm.ErrorfToolOut("color_scheme is now %s, but the page did not show it in time: %w", input.ColorScheme, err)
	}
	if scheme == "" {
		return llm.ToolOut{LLMContent: llm.TextContent(fmt.Sprintf("Color scheme override cleared; pages see the browser's prefers-color-scheme: %s.", got))}
	}
	return llm.ToolOut{LLMContent: llm.TextContent(fmt.Sprintf("Pages see prefers-color-scheme: %s, across navigations, until changed.", scheme))}
}

func (b *BrowseTools) emulateMedia(ctx context.Context, input emulateInput) llm.ToolOut {
	switch input.Media {
	case "", "screen", "print":
	default:
		return llm.ErrorfToolOut(`media must be "screen", "print", or empty to clear, got %q; to set prefers-color-scheme, use emulate_color_scheme`, input.Media)
	}
	b.mediaMu.Lock()
	defer b.mediaMu.Unlock()
	m := b.media
	m.Type = input.Media
	b.saveMedia(m)
	if err := b.browserRun(ctx, input.Timeout, ""); err != nil {
		return llm.ErrorfToolOut("media type is now %q, but Chrome did not confirm it in time: %w", input.Media, err)
	}

	if input.Media == "" {
		return llm.ToolOut{LLMContent: llm.TextContent("Media type emulation cleared")}
	}
	return llm.ToolOut{LLMContent: llm.TextContent(fmt.Sprintf("Media type set to %q", input.Media))}
}
