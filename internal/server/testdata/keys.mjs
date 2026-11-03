// Drives the rendered page in headless chromium over the DevTools protocol and
// checks the keyboard. Run by TestPageKeys: node keys.mjs <chromium> <page.html>.
// window.open and fetch are stubbed, so nothing navigates or posts; the script
// prints one line per failed check and exits non-zero if there was any.
import { spawn } from "node:child_process";
import { mkdtempSync } from "node:fs";
import { tmpdir } from "node:os";
import { join } from "node:path";
import { pathToFileURL } from "node:url";

const [chromium, page] = process.argv.slice(2);
const browser = spawn(chromium, [
    "--headless=new", "--no-sandbox", "--disable-gpu", "--remote-debugging-port=0",
    "--user-data-dir=" + mkdtempSync(join(tmpdir(), "elly-keys-")), "about:blank",
], { stdio: ["ignore", "ignore", "pipe"] });
const fail = (msg) => { console.log(msg); process.exitCode = 1; };
const timer = setTimeout(() => { fail("timed out"); browser.kill(); process.exit(1); }, 30000);

const wsUrl = await new Promise((resolve, reject) => {
    let buf = "";
    browser.stderr.on("data", (d) => {
        buf += d;
        const m = buf.match(/DevTools listening on (ws:\S+)/);
        if (m) resolve(m[1]);
    });
    browser.on("exit", () => reject(new Error("chromium exited: " + buf)));
});

const ws = new WebSocket(wsUrl);
await new Promise((r) => ws.addEventListener("open", r));
let id = 0;
const pending = new Map();
const waiters = [];
ws.addEventListener("message", (e) => {
    const msg = JSON.parse(e.data);
    if (msg.id && pending.has(msg.id)) {
        pending.get(msg.id)(msg);
        pending.delete(msg.id);
    } else if (msg.method) {
        waiters.filter((w) => w.method === msg.method).forEach((w) => w.resolve(msg));
    }
});
const send = (method, params = {}, sessionId) => new Promise((resolve, reject) => {
    const n = ++id;
    pending.set(n, (msg) => msg.error ? reject(new Error(method + ": " + msg.error.message)) : resolve(msg.result));
    ws.send(JSON.stringify({ id: n, method, params, sessionId }));
});
const next = (method) => new Promise((resolve) => waiters.push({ method, resolve }));

const { targetId } = await send("Target.createTarget", { url: "about:blank" });
const { sessionId } = await send("Target.attachToTarget", { targetId, flatten: true });
const s = (method, params) => send(method, params, sessionId);
await s("Page.enable");
await s("Page.addScriptToEvaluateOnNewDocument", { source: `
    window.__opened = [];
    window.__fetched = [];
    window.open = (u) => { window.__opened.push(String(u)); return null; };
    window.fetch = (u, o) => { window.__fetched.push((o && o.method || "GET") + " " + u); return new Promise(() => {}); };
` });
const loaded = next("Page.loadEventFired");
await s("Page.navigate", { url: pathToFileURL(page).href });
await loaded;

const codes = { Enter: 13, Escape: 27, ArrowDown: 40, ArrowUp: 38 };
const press = async (key) => {
    const printable = key.length === 1;
    const base = {
        key,
        code: printable ? "Key" + key.toUpperCase() : key,
        windowsVirtualKeyCode: codes[key] || key.toUpperCase().charCodeAt(0),
        modifiers: key === "?" ? 8 : 0,
    };
    await s("Input.dispatchKeyEvent", { type: "keyDown", ...base, text: printable ? key : key === "Enter" ? "\r" : undefined });
    await s("Input.dispatchKeyEvent", { type: "keyUp", ...base });
};
const value = async (expr) => (await s("Runtime.evaluate", { expression: expr, returnByValue: true })).result.value;
const expect = async (what, expr, want) => {
    const got = await value(expr);
    if (JSON.stringify(got) !== JSON.stringify(want)) fail(`${what}: got ${JSON.stringify(got)}, want ${JSON.stringify(want)}`);
};
const focused = "document.activeElement.closest('.pr') && document.activeElement.closest('.pr').id";

await press("j");
await expect("j focuses the first row", focused, "pr-o-r-1");
await press("j");
await expect("j moves to the next row", focused, "pr-o-r-2");
await press("ArrowDown");
await press("j");
await press("j");
await expect("j stops at the last row outside folded groups", focused, "pr-o-r-3");
await press("k");
await expect("k moves back", focused, "pr-o-r-2");
await press("ArrowUp");
await expect("ArrowUp moves back", focused, "pr-o-r-1");
await press("j");
await press("Enter");
await expect("Enter opens the focused PR", "window.__opened", ["https://github.com/o/r/pull/2"]);
await expect("Enter does not toggle the row", "document.getElementById('pr-o-r-2').classList.contains('open')", false);
await press("o");
await expect("o opens the row's details", "document.getElementById('pr-o-r-2').classList.contains('open')", true);
await press("b");
await expect("b buries the focused PR", "window.__fetched.filter((f) => f.includes('/bury') || f.includes('/unbury')).length", 1);
await press("r");
await expect("r polls", "window.__fetched.includes('POST /api/v0/prs/refresh')", true);
await press("?");
await expect("? opens the key list", "document.querySelector('dialog.keys').open", true);
await press("Escape");
await expect("Escape closes it", "document.querySelector('dialog.keys').open", false);

await value("document.getElementById('filter').focus()");
const before = await value("window.__fetched.length");
for (const k of ["j", "k", "b", "r", "o"]) await press(k);
await expect("keys typed into the filter stay text", "document.getElementById('filter').value", "jkbro");
await expect("and do nothing else", "window.__fetched.length", before);
await value("document.getElementById('filter').value = ''; document.getElementById('filter').dispatchEvent(new Event('input'))");
await value("document.querySelector('details.token').open = true; document.getElementById('pat-input').focus()");
await press("r");
await expect("keys typed into the token field stay text", "document.getElementById('pat-input').value", "r");
await expect("and do not poll", "window.__fetched.length", before);

clearTimeout(timer);
ws.close();
browser.kill();
