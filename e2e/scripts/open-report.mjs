#!/usr/bin/env node
// Opens report/index.html in the default browser after a run — best
// effort only: no display (a headless box, a sandboxed agent) or no
// opener binary just means this quietly does nothing, it never fails the
// npm script or make target that called it.
import { spawn } from 'node:child_process';
import fs from 'node:fs';
import path from 'node:path';
import { fileURLToPath } from 'node:url';

const __dirname = path.dirname(fileURLToPath(import.meta.url));
const reportPath = path.join(__dirname, '..', 'report', 'index.html');

if (!fs.existsSync(reportPath)) {
  console.error(`no report to open at ${path.relative(process.cwd(), reportPath)} — run report:build first.`);
  process.exit(0);
}

// Linux only opens if there's an actual graphical session to open into —
// xdg-open exists on plenty of headless boxes too and will just hang or
// error there. macOS/Windows always have a GUI when a process can run at
// all, so no such check is needed for them.
if (process.platform === 'linux' && !process.env.DISPLAY && !process.env.WAYLAND_DISPLAY) {
  console.log(`no graphical session detected — open it manually: ${reportPath}`);
  process.exit(0);
}

const [cmd, args] =
  process.platform === 'darwin' ? ['open', [reportPath]] :
  process.platform === 'win32' ? ['cmd', ['/c', 'start', '""', reportPath]] :
  ['xdg-open', [reportPath]];

const child = spawn(cmd, args, { detached: true, stdio: 'ignore' });
child.on('error', (err) => {
  console.log(`couldn't auto-open (${err.message}) — open it manually: ${reportPath}`);
});
child.unref();
