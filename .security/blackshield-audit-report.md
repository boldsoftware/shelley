# BlackShield Security Audit Report — `boldsoftware/shelley`

- **Scanned Commit:** `6c0321cf5b733567ea68bb7b9b7ad27b84e7632d`
- **Actionable Findings:** `22` (`4 Critical`, `18 High`, `14 Medium`, `2 Low`)
- **BlackShield Platform & Continuous Zero-Cost PR Protection:** [https://blackshield.chaplau.com](https://blackshield.chaplau.com)

## Detailed Findings & Remediation Guide

### 1. [CRITICAL] `sec-cmdi-shell` — sec-cmdi-shell: Command Injection via Shell Invocation

- **Category:** `sast`
- **Location:** `claudetool/bash.go:319`
- **Details:** Executing system commands through a shell interpreter with dynamic arguments permits command injection. at claudetool/bash.go:319
- **Recommended Fix:** Invoke the target binary directly with exec.Command(bin, args...) without sh -c shell evaluation.

### 2. [CRITICAL] `sec-cmdi-go-taint` — sec-cmdi-go-taint: Go Command Injection via Tainted Variable

- **Category:** `sast`
- **Location:** `server/git_handlers.go:965`
- **Details:** Tainted variable 'from' passed to exec.Command sink. at server/git_handlers.go:965
- **Recommended Fix:** Validate command arguments against an allowlist and avoid passing user input to shell interpreters.

### 3. [CRITICAL] `sec-cmdi-go-taint` — sec-cmdi-go-taint: Go Command Injection via Tainted Variable

- **Category:** `sast`
- **Location:** `server/git_handlers.go:1340`
- **Details:** Tainted variable 'hash' passed to exec.Command sink. at server/git_handlers.go:1340
- **Recommended Fix:** Validate command arguments against an allowlist and avoid passing user input to shell interpreters.

### 4. [CRITICAL] `sec-cmdi-go-taint` — sec-cmdi-go-taint: Go Command Injection via Tainted Variable

- **Category:** `sast`
- **Location:** `server/git_handlers.go:1356`
- **Details:** Tainted variable 'hash' passed to exec.Command sink. at server/git_handlers.go:1356
- **Recommended Fix:** Validate command arguments against an allowlist and avoid passing user input to shell interpreters.

### 5. [HIGH] `go-path-traversal-os-open` — go-path-traversal-os-open: Path Traversal via os.Open with User Input

- **Category:** `sast`
- **Location:** `claudetool/bashjob_linux.go:16`
- **Details:** Go application security vulnerability or insecure coding pattern. at claudetool/bashjob_linux.go:16
- **Recommended Fix:** Clean paths with filepath.Clean() and verify strings.HasPrefix against the intended base directory.

### 6. [HIGH] `sec-path-traversal` — sec-path-traversal: Path Traversal / Arbitrary File Access

- **Category:** `sast`
- **Location:** `db/conversation_import.go:27`
- **Details:** Accessing filesystem paths constructed from unvalidated input allows path traversal. at db/conversation_import.go:27
- **Recommended Fix:** Sanitize user-provided file paths with filepath.Clean or secure_filename before reading.

### 7. [HIGH] `sec-sqli-format` — sec-sqli-format: SQL Injection via Formatted String Query

- **Category:** `sast`
- **Location:** `db/conversation_vms.go:286`
- **Details:** Constructing SQL queries with string formatting or concatenation can lead to SQL injection. at db/conversation_vms.go:286
- **Recommended Fix:** Use parameterized queries or prepared statements instead of string concatenation.

### 8. [HIGH] `sec-path-traversal` — sec-path-traversal: Path Traversal / Arbitrary File Access

- **Category:** `sast`
- **Location:** `db/db.go:226`
- **Details:** Accessing filesystem paths constructed from unvalidated input allows path traversal. at db/db.go:226
- **Recommended Fix:** Sanitize user-provided file paths with filepath.Clean or secure_filename before reading.

### 9. [HIGH] `sec-sqli-format` — sec-sqli-format: SQL Injection via Formatted String Query

- **Category:** `sast`
- **Location:** `db/model_providers.go:46`
- **Details:** Constructing SQL queries with string formatting or concatenation can lead to SQL injection. at db/model_providers.go:46
- **Recommended Fix:** Use parameterized queries or prepared statements instead of string concatenation.

### 10. [HIGH] `sec-sqli-format` — sec-sqli-format: SQL Injection via Formatted String Query

- **Category:** `sast`
- **Location:** `db/model_providers.go:45`
- **Details:** Constructing SQL queries with string formatting or concatenation can lead to SQL injection. at db/model_providers.go:45
- **Recommended Fix:** Use parameterized queries or prepared statements instead of string concatenation.

### 11. [HIGH] `sec-sqli-format` — sec-sqli-format: SQL Injection via Formatted String Query

- **Category:** `sast`
- **Location:** `db/projects.go:229`
- **Details:** Constructing SQL queries with string formatting or concatenation can lead to SQL injection. at db/projects.go:229
- **Recommended Fix:** Use parameterized queries or prepared statements instead of string concatenation.

### 12. [HIGH] `sec-sqli-format` — sec-sqli-format: SQL Injection via Formatted String Query

- **Category:** `sast`
- **Location:** `db/workspace.go:392`
- **Details:** Constructing SQL queries with string formatting or concatenation can lead to SQL injection. at db/workspace.go:392
- **Recommended Fix:** Use parameterized queries or prepared statements instead of string concatenation.

### 13. [HIGH] `sec-sqli-format` — sec-sqli-format: SQL Injection via Formatted String Query

- **Category:** `sast`
- **Location:** `db/workspace.go:540`
- **Details:** Constructing SQL queries with string formatting or concatenation can lead to SQL injection. at db/workspace.go:540
- **Recommended Fix:** Use parameterized queries or prepared statements instead of string concatenation.

### 14. [HIGH] `sec-sqli-format` — sec-sqli-format: SQL Injection via Formatted String Query

- **Category:** `sast`
- **Location:** `db/workspace.go:391`
- **Details:** Constructing SQL queries with string formatting or concatenation can lead to SQL injection. at db/workspace.go:391
- **Recommended Fix:** Use parameterized queries or prepared statements instead of string concatenation.

### 15. [HIGH] `sec-path-traversal` — sec-path-traversal: Path Traversal / Arbitrary File Access

- **Category:** `sast`
- **Location:** `demo.py:71`
- **Details:** Accessing filesystem paths constructed from unvalidated input allows path traversal. at demo.py:71
- **Recommended Fix:** Sanitize user-provided file paths with filepath.Clean or secure_filename before reading.

### 16. [HIGH] `go-cmdi-exec-combined` — go-cmdi-exec-combined: Command Execution via Dynamic exec.Command

- **Category:** `sast`
- **Location:** `lazycue/video.go:191`
- **Details:** Go application security vulnerability or insecure coding pattern. at lazycue/video.go:191
- **Recommended Fix:** Invoke the target binary directly with exec.Command(bin, args...) without sh -c shell evaluation.

### 17. [HIGH] `py-ssrf-urllib-urlopen` — py-ssrf-urllib-urlopen: SSRF via urllib.request.urlopen

- **Category:** `sast`
- **Location:** `scripts/extract-headless-shell.py:26`
- **Details:** Unvalidated user-controlled URLs in HTTP client requests cause SSRF. at scripts/extract-headless-shell.py:26
- **Recommended Fix:** Validate target URLs against an explicit hostname allowlist and block private/loopback IP ranges.

### 18. [HIGH] `bs-poly-js-ssrf-needle` — bs-poly-js-ssrf-needle: Server-Side Request Forgery (SSRF) via Outbound HTTP Client

- **Category:** `sast`
- **Location:** `scripts/fetch-exe-scroll.py:34`
- **Details:** Server-Side Request Forgery (SSRF) via Outbound HTTP Client (CWE-918 / A10:2021). at scripts/fetch-exe-scroll.py:34
- **Recommended Fix:** Validate, parameterize, or sanitize untrusted inputs before invoking dangerous sinks.

### 19. [HIGH] `bs-poly-js-ssrf-needle` — bs-poly-js-ssrf-needle: Server-Side Request Forgery (SSRF) via Outbound HTTP Client

- **Category:** `sast`
- **Location:** `server/debug/conversations/conversations.js:13`
- **Details:** Server-Side Request Forgery (SSRF) via Outbound HTTP Client (CWE-918 / A10:2021). at server/debug/conversations/conversations.js:13
- **Recommended Fix:** Validate, parameterize, or sanitize untrusted inputs before invoking dangerous sinks.

### 20. [HIGH] `bs-poly-js-ssrf-needle` — bs-poly-js-ssrf-needle: Server-Side Request Forgery (SSRF) via Outbound HTTP Client

- **Category:** `sast`
- **Location:** `server/debug/timelines/page.js:5`
- **Details:** Server-Side Request Forgery (SSRF) via Outbound HTTP Client (CWE-918 / A10:2021). at server/debug/timelines/page.js:5
- **Recommended Fix:** Validate, parameterize, or sanitize untrusted inputs before invoking dangerous sinks.

### 21. [HIGH] `sec-xss-raw-html` — sec-xss-raw-html: Cross-Site Scripting (XSS) via Unsanitized HTML

- **Category:** `sast`
- **Location:** `server/debug/timelines/timeline.js:54`
- **Details:** Rendering unescaped HTML directly in UI templates exposes web applications to script injection. at server/debug/timelines/timeline.js:54
- **Recommended Fix:** Contextually encode output (htmlspecialchars, DOMPurify) and specify an explicit targetOrigin in postMessage.

### 22. [HIGH] `sec-xss-raw-html` — sec-xss-raw-html: Cross-Site Scripting (XSS) via Unsanitized HTML

- **Category:** `sast`
- **Location:** `server/debug/timelines/timeline.js:98`
- **Details:** Rendering unescaped HTML directly in UI templates exposes web applications to script injection. at server/debug/timelines/timeline.js:98
- **Recommended Fix:** Contextually encode output (htmlspecialchars, DOMPurify) and specify an explicit targetOrigin in postMessage.

