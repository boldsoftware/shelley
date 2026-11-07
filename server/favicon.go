package server

import (
	"context"
	"encoding/json"
	"fmt"
	"html"
	"math/rand/v2"
	"net/http"
	"net/url"
	"strings"
	"unicode"
	"unicode/utf8"

	"github.com/rivo/uniseg"

	"shelley.exe.dev/db"
)

const faviconEmojiSettingKey = "favicon_emoji"

// safeFaviconEmojis are the candidates for a randomly chosen favicon. Each is
// a single code point with default emoji presentation from Unicode 11 or
// earlier, so it renders as a color glyph on any modern system, and none is
// a person, hand, flag, status symbol, or anything a reasonable user would
// rather not have on a work tab.
var safeFaviconEmojis = []string{
	// Animals
	"🐙", "🦊", "🐻", "🐼", "🐨", "🐯", "🦁", "🐮", "🐸", "🐵",
	"🐧", "🐦", "🦉", "🦄", "🐝", "🐞", "🦋", "🐢", "🦎", "🦖",
	"🦕", "🐳", "🐬", "🐟", "🐠", "🦀", "🦑", "🦞", "🦔", "🦒",
	"🦓", "🦘", "🦙", "🐘", "🦛", "🐪", "🐇", "🐹", "🐱", "🐶",
	"🐌", "🦜", "🦚", "🐲", "🐚",
	// Plants
	"🌵", "🌲", "🌳", "🌴", "🌱", "🌿", "🍀", "🍁", "🍄", "🌻",
	"🌷", "🌸", "🌺", "🌼", "🌾",
	// Food
	"🍎", "🍊", "🍋", "🍌", "🍉", "🍇", "🍓", "🍒", "🍍", "🥝",
	"🥑", "🥕", "🌽", "🥨", "🥐", "🥯", "🧀", "🍕", "🌮", "🍩",
	"🍪", "🧁", "🍦", "🍿", "🥥",
	// Nature and space
	"🌙", "🌈", "🌊", "⭐", "⚡", "🔥", "🌍", "🌋", "🗻",
	// Things
	"🎈", "🎨", "🎸", "🎹", "🎺", "🎻", "🥁", "🎲", "🧩", "🚀",
	"🛸", "⛵", "🚲", "🏀", "⚽", "🎾", "⚾", "🎯", "🔮", "💎",
	"🔑", "🔭", "🔬", "🧭", "🧲", "💡", "📚", "🎁", "🏆",
	"👑", "🎩", "🧸", "🏰", "🎡", "⛺",
}

// faviconEmoji is the emoji shown in the favicon and where it comes from:
// "exe.dev" when the VM's reflection metadata names one, otherwise "shelley"
// for the emoji stored in Shelley's own database.
type faviconEmoji struct {
	Emoji  string `json:"emoji"`
	Source string `json:"source"`
}

// currentFaviconEmoji returns the VM's exe.dev emoji when there is one.
// Otherwise it returns Shelley's stored emoji, choosing and storing a random
// safe one the first time it is needed.
func (s *Server) currentFaviconEmoji(ctx context.Context) (faviconEmoji, error) {
	if emoji := s.reflectionEmoji(ctx); emoji != "" {
		return faviconEmoji{Emoji: emoji, Source: "exe.dev"}, nil
	}
	emoji, err := s.db.GetSetting(ctx, faviconEmojiSettingKey)
	if err != nil {
		return faviconEmoji{}, fmt.Errorf("get favicon emoji: %w", err)
	}
	if emoji == "" {
		random := safeFaviconEmojis[rand.IntN(len(safeFaviconEmojis))]
		emoji, err = s.db.InitSetting(ctx, faviconEmojiSettingKey, random)
		if db.IsDiskFull(err) {
			// Serve the page anyway, or a full disk hides the whole UI,
			// low-disk notice included. A later load stores an emoji.
			s.logger.Error("Failed to store favicon emoji; using it unsaved", "error", err)
			emoji = random
		} else if err != nil {
			return faviconEmoji{}, fmt.Errorf("init favicon emoji: %w", err)
		}
	}
	return faviconEmoji{Emoji: emoji, Source: "shelley"}, nil
}

func faviconHref(emoji string) string {
	svg := fmt.Sprintf(`<svg xmlns="http://www.w3.org/2000/svg" viewBox="0 0 400 400">
<text x="200" y="200" text-anchor="middle" dominant-baseline="central" font-size="320" font-family="Apple Color Emoji, Segoe UI Emoji, Noto Color Emoji, sans-serif">%s</text>
</svg>`, html.EscapeString(emoji))
	return "data:image/svg+xml," + url.PathEscape(svg)
}

// validFaviconEmoji reports whether s is one visible grapheme: an emoji,
// including ZWJ sequences, skin tones, keycaps, and flags, or any other single
// symbol. It must start with a visible base (not a lone joiner, variation
// selector, space, or blank filler letter), and every rune must be graphic or
// a format character such as a ZWJ or tag, which also keeps out runes XML
// forbids.
func validFaviconEmoji(s string) bool {
	if !utf8.ValidString(s) || len(s) > 64 || uniseg.GraphemeClusterCount(s) != 1 {
		return false
	}
	base, _ := utf8.DecodeRuneInString(s)
	if !unicode.In(base, unicode.L, unicode.N, unicode.P, unicode.S) ||
		unicode.Is(unicode.Other_Default_Ignorable_Code_Point, base) || // e.g. Hangul fillers
		base == '\u2800' { // Braille blank
		return false
	}
	for _, r := range s {
		if !unicode.IsGraphic(r) && !unicode.Is(unicode.Cf, r) {
			return false
		}
	}
	return true
}

type faviconEmojiResponse struct {
	faviconEmoji
	Href string `json:"href"`
}

// handleGetFaviconEmoji handles GET /api/favicon-emoji.
func (s *Server) handleGetFaviconEmoji(w http.ResponseWriter, r *http.Request) {
	fe, err := s.currentFaviconEmoji(r.Context())
	if err != nil {
		s.internalError(w, "Failed to get favicon emoji", err)
		return
	}
	writeJSON(w, http.StatusOK, faviconEmojiResponse{fe, faviconHref(fe.Emoji)})
}

// handleSetFaviconEmoji handles PUT /api/favicon-emoji, storing Shelley's
// emoji. It takes effect only while exe.dev does not supply one, and the
// response reports whichever emoji is in effect.
func (s *Server) handleSetFaviconEmoji(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Emoji string `json:"emoji"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, "Invalid request body", http.StatusBadRequest)
		return
	}
	emoji := strings.TrimSpace(req.Emoji)
	if !validFaviconEmoji(emoji) {
		http.Error(w, "Favicon emoji must be a single emoji", http.StatusBadRequest)
		return
	}
	if err := s.db.SetSetting(r.Context(), faviconEmojiSettingKey, emoji); err != nil {
		s.internalError(w, "Failed to set favicon emoji", err)
		return
	}
	s.handleGetFaviconEmoji(w, r)
}
