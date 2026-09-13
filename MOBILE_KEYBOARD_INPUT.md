# Keep the mobile composer above the keyboard

Android Chrome can cover the message textarea with its software keyboard even
though the composer's toolbar remains visible. The app uses a full-height,
overflow-hidden layout. Resizing only the **visual** viewport leaves that layout
at its old height; scrolling the textarea does not resize the app.

The page now requests `interactive-widget=resizes-content` in its viewport meta
tag so supporting browsers resize both the visual and layout viewports when the
keyboard opens. This lets the existing flex layout and viewport units keep the
composer above the keyboard. No JS keyboard-height estimates, fixed keyboard
sizes, or extra resize listeners are introduced. The existing focus handling is
unchanged, including on browsers that do not implement this directive.

This targets the reported Android Chrome behavior, not a claim of a new Safari
keyboard fix. See Chrome's documented [keyboard viewport resize behavior](https://developer.chrome.com/blog/viewport-resize-behavior).

## Regression checks

`ui/e2e/mobile-keyboard-input.spec.ts` checks the policy in the served HTML and
exercises new and existing conversations at portrait, keyboard-sized, and
landscape viewport sizes. It checks the entire textarea and action buttons,
multiline draft preservation, focus, keyboard dismissal, and sending.

Headless mobile emulation cannot open a real Android software keyboard. Merely
resizing a Playwright viewport would pass without fixing the original browser
policy, so the HTML policy assertion is essential and fails without the fix.
The resized-layout tests are not presented as a physical-device keyboard test.

After `make build-custom`, run from `ui/`:

```sh
pnpm exec playwright test e2e/mobile-keyboard-input.spec.ts e2e/message-input-alignment.spec.ts
```

Before deployment, check on an Android phone with Chrome:

1. Open a new conversation and tap the input. Type several lines and confirm
   the text/caret and send button are above the keyboard.
2. Repeat in an existing conversation with a long message history.
3. Rotate the phone, dismiss and reopen the keyboard, and confirm the draft
   remains intact and the input stays usable. Long drafts should scroll inside
   the textarea rather than push it below the keyboard.
4. Send from the visible button. Check the normal desktop composer too.
