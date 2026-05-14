---
name: reflection-integration
description: Use to discover external services and APIs available on this exe.dev VM via network-edge-injected credentials and to discover VM metadata like owner email, VM tags, and default port.
when: exe.dev
---

Most exe.dev VMs have a `reflection` endpoint available.

Start with

```
curl https://reflection.int.exe.xyz/
```

and explore from there.

If this fails, the VM may be old, or the user may have removed the reflection integration or given it an unusual name.

Integrations CRUD (user only): https://exe.dev/integrations.
