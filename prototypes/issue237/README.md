# Model routing notices — #237

Prototype of an inline notice when the provider serves a different model. C's compact icon, bold heading, model chips, and expandable details are wired into the warning renderer, with amber for provider routing and neutral colors for recovery. Run only in the scratch preview; the main instance is unchanged.

## Working preview

With the predictable model, send `routing demo: opus` twice, then `routing demo: fable`. The first response emits a routing warning, the second stays quiet, and the third emits a recovery notice. `routing demo: snapshot` exercises equivalent dated model IDs; `routing demo: unknown` supplies no served model. All are simulated metadata: no Claude requests are made.

The loop compares the requested ID in response origin with the served response model. Known Claude IDs are normalized by family and major/minor version, ignoring snapshot and hosting suffixes. Warning messages are excluded from model context. Tracking survives turns within one loop, not loop recreation. Unknown and non-Claude IDs do not trigger notices.

Notices are recorded when the response completes, before its persisted message—not before its first streamed token. Routing notices bypass the retry-warning cap. Notice and reply persistence use a cancellation-independent context; if a notice write fails, the completed reply is still recorded before the error is returned.

## Visual comparison

The conversation markup was captured from the running Vue message renderer; replies and model transitions are simulated.

Compare three treatments: warning, timeline, and model marker. Scenario controls show provider routing, a return to the requested model, and an intentional user switch. Details distinguish the selected model from the response model without inventing a routing reason.

`hybrid.html` stages C's compact icon, bold heading, and expandable details with amber warning colors. It shows collapsed and expanded copies side by side; recovery and intentional changes remain neutral. Use it instead of `stage.html` in the request below to create that variation.

`stage.html` and `stage.css` render against Shelley's built styles in a scoped `shelley-ui-dev` live stage. To recreate one with the development proxy running on port 8040:

```sh
python3 - <<'PY'
import json
from pathlib import Path
from urllib.request import Request, urlopen

root = Path('prototypes/issue237')
payload = json.dumps({
    'name': '#237 · Model routing notices',
    'html': (root / 'stage.html').read_text(),
    'css': (root / 'stage.css').read_text(),
}).encode()
request = Request('http://localhost:8040/__stage__/live', payload,
                  {'Content-Type': 'application/json'})
with urlopen(request) as response:
    print(response.read().decode())
PY
```

Open `/__stage__/live/<id>` using the returned stage ID. The stage is disposable and uses the proxy's scratch conversation backend.
