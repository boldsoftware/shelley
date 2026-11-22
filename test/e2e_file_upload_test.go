package test

import (
	"net/http"
	"sync"
	"testing"
)

// Ported from ui/e2e/file-upload.spec.ts.

// dropFilesJS gives pages window.dropFiles(files), which drops files on the
// composer as a drag from the desktop would: a "drop" DragEvent on
// .message-input-container whose dataTransfer holds a text/plain File for
// every [name, content] pair.
const dropFilesJS = `window.dropFiles = (files) => {
  const dataTransfer = new DataTransfer();
  for (const [name, content] of files) {
    dataTransfer.items.add(new File([new Blob([content], { type: 'text/plain' })], name, { type: 'text/plain' }));
  }
  document.querySelector('.message-input-container').dispatchEvent(
    new DragEvent('drop', { bubbles: true, cancelable: true, dataTransfer }));
};`

const dropFilesSetup = `window.dropFiles(files) drops files on the composer: it dispatches a "drop" DragEvent carrying a text/plain File for each [name, content] pair on ".message-input-container"; call it in an eval, e.g. window.dropFiles([["a.txt", "some content"]]). `

func TestE2EFileUploadDragOverlay(t *testing.T) {
	e2e(t, useful(0), `Dragging a file over the composer shows the drop overlay. 1. Navigate to /new and wait for the message input container (".message-input-container") to be visible. 2. In an eval, dispatch on that container new DragEvent("dragenter", {bubbles: true, cancelable: true, dataTransfer: new DataTransfer()}). 3. The drop overlay (".drag-overlay") should become visible, and the overlay element itself must contain the text "Drop files here" (assert on ".drag-overlay", not on the whole page). 4. In an eval, dispatch on the container new DragEvent("dragleave", {bubbles: true, cancelable: true, dataTransfer: new DataTransfer()}). 5. The drop overlay should become hidden.`, nil)
}

func TestE2EFileUploadEndpointReturnsPath(t *testing.T) {
	e2e(t, useful(0), `The upload endpoint accepts files and returns their path. 1. Navigate to /new. 2. In one eval, POST to /api/upload with fetch a multipart form (FormData) whose field "file" is a file named "test.txt" of type text/plain containing "test file content", read the response's JSON, and return JSON.stringify({status: response.status, path: typeof json.path, uploads: json.path.includes("/tmp/shelley-uploads/"), txt: json.path.includes(".txt")}). It must be exactly {"status":200,"path":"string","uploads":true,"txt":true}.`, nil)
}

func TestE2EFileUploadReadBack(t *testing.T) {
	e2e(t, useful(0), `An uploaded file can be read back with the /api/read endpoint. 1. Navigate to /new. 2. In one eval: POST to /api/upload with fetch a multipart form (FormData) whose field "file" is a file named "readable.txt" of type text/plain containing "hello from test"; take the path from the response's JSON; then GET /api/read?path= followed by encodeURIComponent of that path; and return JSON.stringify({upload: <the upload response's status>, read: <the read response's status>, text: <the read response's text>}). It must be exactly {"upload":200,"read":200,"text":"hello from test"}.`, nil)
}

func TestE2EFileUploadTextInput(t *testing.T) {
	e2e(t, useful(0), `The message input accepts text normally. Navigate to /new, fill the message input (data-testid "message-input") with "Hello, this is a test message", and its value should be exactly "Hello, this is a test message".`, nil)
}

func TestE2EFileUploadPickerAcceptsAnyType(t *testing.T) {
	e2e(t, useful(0), `The file picker does not restrict file types. Navigate to /new and wait for the message input (data-testid "message-input") to be visible. There should be exactly one hidden file input, input[type="file"].message-input-hidden, and it must have no accept attribute at all (hasAttribute("accept") is false).`, nil)
}

func TestE2EFileUploadDropShowsChip(t *testing.T) {
	e2e(t, useful(0), dropFilesSetup+`A dropped file shows as an attachment chip, not as text. 1. Navigate to /new and wait for the message input (data-testid "message-input") to be visible. 2. Drop one file, "test-drop.txt" containing "test content". 3. There should be exactly one attachment chip (".message-attachment"). 4. The message input must still be empty: eval JSON.stringify of its value, which should be exactly "" (two double-quote characters; an empty expectation would check nothing).`, func(s *shelley) {
		s.initScript(dropFilesJS)
	})
}

func TestE2EFileUploadDropMultiple(t *testing.T) {
	e2e(t, useful(0), dropFilesSetup+`Dropping several files creates an attachment for each. 1. Navigate to /new and wait for the message input (data-testid "message-input") to be visible. 2. Drop three files at once, in one call: "drop-0.txt" containing "content 0", "drop-1.txt" containing "content 1" and "drop-2.txt" containing "content 2". 3. There should be exactly three attachment chips (".message-attachment").`, func(s *shelley) {
		s.initScript(dropFilesJS)
	})
}

func TestE2EFileUploadKeepsUnsentAttachments(t *testing.T) {
	e2e(t, useful(0), dropFilesSetup+`Unsent attachments stay with their conversation. There are two conversations: /c/attachment-first, whose drawer item is [data-conversation-id="{{FIRST_ID}}"], and an archived one whose drawer item is [data-conversation-id="{{SECOND_ID}}"]. The server's answer to uploads (/api/upload) is held back until call "release". Perform the steps in order. 1. Navigate to /c/attachment-first and wait (up to 30s) for the message input (data-testid "message-input") to be visible. 2. Drop one file, "draft.txt" containing "draft attachment". 3. There should be exactly one uploading attachment (".message-attachment-uploading"). 4. Open the conversations drawer (click button[aria-label="Open conversations"]; it is visible on this phone). 5. Click the "View archived" button, then click the archived conversation's item [data-conversation-id="{{SECOND_ID}}"]. 6. The message input should become hidden, and there must be no ".message-attachment" at all (count 0). 7. Call "release". 8. Open the conversations drawer again the same way, click the "Back to conversations" button, then click [data-conversation-id="{{FIRST_ID}}"]. 9. There should be exactly one ready attachment (".message-attachment-ready"), and the attachment name (".message-attachment-name") should be exactly "draft.txt".`, func(s *shelley) {
		s.initScript(dropFilesJS)
		first := s.conversation("attachment draft first", "attachment-first")
		second := s.conversation("attachment draft second", "attachment-second")
		s.api("POST", "/api/conversation/"+second.ID+"/archive", nil, nil)
		s.v("FIRST_ID", first.ID)
		s.v("SECOND_ID", second.ID)
		gate := make(chan struct{})
		release := sync.OnceFunc(func() { close(gate) })
		s.route("/api/upload", func(w http.ResponseWriter, r *http.Request) {
			select {
			case <-gate:
			case <-r.Context().Done():
				return
			}
			s.pass.ServeHTTP(w, r)
		})
		s.call("release", func() string {
			release()
			return "released"
		})
	})
}

func TestE2EFileUploadDiscardsAbandonedNew(t *testing.T) {
	e2e(t, useful(0), dropFilesSetup+`Attachments dropped on an abandoned new conversation are discarded. There is a conversation whose drawer item is [data-conversation-id="{{ID}}"]. Perform the steps in order. 1. Navigate to /new and wait for the composer (".message-input-container") to be visible. 2. Drop one file, "stale.txt" containing "stale". 3. There should be exactly one ready attachment (".message-attachment-ready"). 4. Open the conversations drawer (click button[aria-label="Open conversations"]; it is visible on this phone) and click the conversation's item [data-conversation-id="{{ID}}"]; wait for the URL to leave /new. 5. Start a new conversation from the header: click the "New Conversation" button in ".header" if it is visible; on a phone it is not, so instead click the "More options" button and then the "New Conversation" button in ".chat-overflow-popover". 6. Wait for the URL to end in /new and the message input (data-testid "message-input") to be visible. 7. There must be no attachment chips (".message-attachment" count 0).`, func(s *shelley) {
		s.initScript(dropFilesJS)
		c := s.conversation("attachment destination", "attachment-destination")
		s.v("ID", c.ID)
	})
}

func TestE2EFileUploadPasteKeepsFocus(t *testing.T) {
	e2e(t, useful(0), `Focus stays in the message input after pasting an image. Perform the steps in order. 1. Navigate to /new and wait for the message input (data-testid "message-input") to be visible. 2. Fill it with "Testing paste focus: " (note the trailing space), which focuses it. 3. In an eval, dispatch on the message input new ClipboardEvent("paste", {clipboardData: dt, bubbles: true, cancelable: true}), where dt is a new DataTransfer holding new File([new Blob(["test"], {type: "image/png"})], "test-paste.png", {type: "image/png"}). 4. Wait 100ms. 5. The message input must be the focused element (document.activeElement === the message input is true). 6. Its value must still be exactly "Testing paste focus: ". 7. There should be exactly one attachment chip (".message-attachment").`, nil)
}
