---
name: reflection-integration
description: Use when a task on an exe.dev VM needs metadata about itself — owner email, attached integrations, VM tags, or comment.
when: exe.dev
---

New exe.dev users get a default integration named `reflection`, attached to all VMs via `auto:all`. From the VM:

```
curl https://reflection.int.exe.xyz/
```

Returns a JSON index of available endpoints. Subpaths (all return JSON):

- `/email` — owner's exe.dev email
- `/integrations` — name, type, help string, and comment for each integration attached to this VM
- `/tags` — VM tags
- `/comment` — the VM's comment

If endpoints fail to resolve, the user may have detached the integration. They can reattach it with:

```
exe.dev ▶ integrations attach reflection auto:all
```
