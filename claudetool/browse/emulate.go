package browse

import (
	"context"
	"encoding/json"
	"fmt"
	"sort"
	"strings"

	"github.com/chromedp/cdproto/emulation"
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

// EmulateTool returns a tool for device and display emulation.
func (b *BrowseTools) EmulateTool() *llm.Tool {
	description := "Device and display emulation. Actions: help, device, custom, reset, dark_mode, media."

	schema := `{
		"type": "object",
		"properties": {
			"action": {
				"type": "string",
				"description": "The emulation action to perform",
				"enum": ["help", "device", "custom", "reset", "dark_mode", "media"]
			},
			"device": {
				"type": "string",
				"description": "Device preset name (device action)"
			},
			"width": {
				"type": "integer",
				"description": "Viewport width in pixels (custom action)"
			},
			"height": {
				"type": "integer",
				"description": "Viewport height in pixels (custom action)"
			},
			"device_scale_factor": {
				"type": "number",
				"description": "Device scale factor / DPR (custom action, default 1.0)"
			},
			"mobile": {
				"type": "boolean",
				"description": "Emulate mobile device (custom action, default false)"
			},
			"touch": {
				"type": "boolean",
				"description": "Enable touch emulation (custom action, default false)"
			},
			"enabled": {
				"type": "boolean",
				"description": "Enable or disable (dark_mode action, default true)"
			},
			"media": {
				"type": "string",
				"description": "CSS media type to emulate, e.g. 'print' or 'screen' (media action)"
			}
		},
		"required": ["action"]
	}`

	return &llm.Tool{
		Name:        "browser_emulate",
		Description: description,
		InputSchema: json.RawMessage(schema),
		Run: func(ctx context.Context, m json.RawMessage) llm.ToolOut {
			var input struct {
				Action string `json:"action"`
			}
			if err := json.Unmarshal(m, &input); err != nil {
				return llm.ErrorfToolOut("invalid input: %w", err)
			}

			switch input.Action {
			case "help":
				return b.emulateHelp()
			case "device":
				return b.emulateDevice(m)
			case "custom":
				return b.emulateCustom(m)
			case "reset":
				return b.emulateReset()
			case "dark_mode":
				return b.emulateDarkMode(m)
			case "media":
				return b.emulateMedia(m)
			default:
				return llm.ErrorfToolOut("unknown action: %q", input.Action)
			}
		},
	}
}

func (b *BrowseTools) emulateHelp() llm.ToolOut {
	var sb strings.Builder
	sb.WriteString("Device Emulation Tool\n")
	sb.WriteString("=====================\n\n")
	sb.WriteString("Actions:\n")
	sb.WriteString("  help      - Show this help message\n")
	sb.WriteString("  device    - Emulate a preset device (param: device)\n")
	sb.WriteString("  custom    - Custom viewport emulation (params: width, height, device_scale_factor, mobile, touch); clears any prior UA override\n")
	sb.WriteString("  reset     - Reset to default viewport (1280x720)\n")
	sb.WriteString("  dark_mode - Toggle automatic dark mode (param: enabled, default true)\n")
	sb.WriteString("  media     - Emulate CSS media type (param: media, e.g. 'print', 'screen')\n")
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

func (b *BrowseTools) emulateDevice(m json.RawMessage) llm.ToolOut {
	var input struct {
		Device string `json:"device"`
	}
	if err := json.Unmarshal(m, &input); err != nil {
		return llm.ErrorfToolOut("invalid input: %w", err)
	}
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

	return b.applyEmulation(preset.Width, preset.Height, preset.DPR, preset.Mobile, preset.Touch, preset.UserAgent)
}

func (b *BrowseTools) emulateCustom(m json.RawMessage) llm.ToolOut {
	var input struct {
		Width             int64   `json:"width"`
		Height            int64   `json:"height"`
		DeviceScaleFactor float64 `json:"device_scale_factor"`
		Mobile            bool    `json:"mobile"`
		Touch             bool    `json:"touch"`
	}
	if err := json.Unmarshal(m, &input); err != nil {
		return llm.ErrorfToolOut("invalid input: %w", err)
	}
	if input.Width <= 0 || input.Height <= 0 {
		return llm.ErrorfToolOut("width and height are required and must be positive")
	}
	if input.DeviceScaleFactor <= 0 {
		input.DeviceScaleFactor = 1.0
	}

	// Passing "" clears any prior UA override, which is correct: custom viewports
	// have no user_agent parameter, so there is no supported way to carry a
	// custom UA forward.
	return b.applyEmulation(input.Width, input.Height, input.DeviceScaleFactor, input.Mobile, input.Touch, "")
}

func (b *BrowseTools) applyEmulation(width, height int64, dpr float64, mobile, touch bool, userAgent string) llm.ToolOut {
	browserCtx, err := b.GetBrowserContext()
	if err != nil {
		return llm.ErrorToolOut(err)
	}

	err = chromedp.Run(browserCtx, chromedp.ActionFunc(func(ctx context.Context) error {
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

func (b *BrowseTools) emulateReset() llm.ToolOut {
	browserCtx, err := b.GetBrowserContext()
	if err != nil {
		return llm.ErrorToolOut(err)
	}

	err = chromedp.Run(browserCtx, chromedp.ActionFunc(func(ctx context.Context) error {
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

func (b *BrowseTools) emulateDarkMode(m json.RawMessage) llm.ToolOut {
	var input struct {
		Enabled *bool `json:"enabled"`
	}
	if err := json.Unmarshal(m, &input); err != nil {
		return llm.ErrorfToolOut("invalid input: %w", err)
	}

	enabled := true
	if input.Enabled != nil {
		enabled = *input.Enabled
	}

	browserCtx, err := b.GetBrowserContext()
	if err != nil {
		return llm.ErrorToolOut(err)
	}

	err = chromedp.Run(browserCtx, chromedp.ActionFunc(func(ctx context.Context) error {
		return emulation.SetAutoDarkModeOverride().WithEnabled(enabled).Do(ctx)
	}))
	if err != nil {
		return llm.ErrorToolOut(err)
	}

	state := "enabled"
	if !enabled {
		state = "disabled"
	}
	return llm.ToolOut{LLMContent: llm.TextContent(fmt.Sprintf("Automatic dark mode %s", state))}
}

func (b *BrowseTools) emulateMedia(m json.RawMessage) llm.ToolOut {
	var input struct {
		Media string `json:"media"`
	}
	if err := json.Unmarshal(m, &input); err != nil {
		return llm.ErrorfToolOut("invalid input: %w", err)
	}

	browserCtx, err := b.GetBrowserContext()
	if err != nil {
		return llm.ErrorToolOut(err)
	}

	err = chromedp.Run(browserCtx, chromedp.ActionFunc(func(ctx context.Context) error {
		return emulation.SetEmulatedMedia().WithMedia(input.Media).Do(ctx)
	}))
	if err != nil {
		return llm.ErrorToolOut(err)
	}

	if input.Media == "" {
		return llm.ToolOut{LLMContent: llm.TextContent("Media type emulation cleared")}
	}
	return llm.ToolOut{LLMContent: llm.TextContent(fmt.Sprintf("Media type set to %q", input.Media))}
}
