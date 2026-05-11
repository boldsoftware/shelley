---
name: node-and-js-frameworks
description: Use when the user needs Node.js or npm installed, or is running a JS framework dev server (Next.js, Vite, webpack, etc.) on an exe.dev VM — especially if the browser logs 'WebSocket ... failed', or the dev server complains about cross-origin requests, allowedDevOrigins, or allowedHosts.
---

## Installing Node.js

Install Node.js using nodeenv via uvx:

```bash
uvx nodeenv -n lts ~/node
mkdir -p ~/.local/bin
ln -sf ~/node/bin/node ~/.local/bin/node
ln -sf ~/node/bin/npm ~/.local/bin/npm
ln -sf ~/node/bin/npx ~/.local/bin/npx
```

This installs the latest LTS version of Node.js into `~/node` and symlinks
the binaries into `~/.local/bin` (which is on `PATH`).

## Running a JS framework dev server on an exe.dev VM

You need to add the full hostname (and hostname:port) to the allowed origins lists.

### Next.js (>= 15.2)

```js
// next.config.js
module.exports = {
  allowedDevOrigins: [
    '<vmname>.exe.xyz',
    '<vmname>.exe.xyz:<port>',
  ],
};
```

```bash
npx next dev -H 0.0.0.0 -p 8000
```

### Vite (>= 5.0)

```js
// vite.config.js
import { defineConfig } from 'vite';

export default defineConfig({
  server: {
    host: '0.0.0.0',
    port: 5173,
    allowedHosts: ['<vmname>.exe.xyz'],
  },
});
```
