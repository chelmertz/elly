// Shared by the page tests: starts headless chromium, opens url in one tab
// over the DevTools protocol, and returns helpers. init runs in the page
// before its own script. A CSP violation is recorded in window.__csp.
import { spawn } from "node:child_process";
import { mkdtempSync } from "node:fs";
import { tmpdir } from "node:os";
import { join } from "node:path";

export async function openPage(chromium, url, init = "") {
    const browser = spawn(chromium, [
        "--headless=new", "--no-sandbox", "--disable-gpu", "--remote-debugging-port=0",
        "--user-data-dir=" + mkdtempSync(join(tmpdir(), "elly-page-")), "about:blank",
    ], { stdio: ["ignore", "ignore", "pipe"] });
    const fail = (msg) => { console.log(msg); process.exitCode = 1; };
    // CI runners have taken 12s for what runs in 3s here, and one ran past
    // 30s; the limit catches a hang, not a slow machine. The message names the
    // last step that finished, so a real hang says where.
    let step = "starting chromium";
    const timer = setTimeout(() => { fail(`timed out after: ${step}`); browser.kill(); process.exit(1); }, 120000);

    const wsUrl = await new Promise((resolve, reject) => {
        let buf = "";
        browser.stderr.on("data", (d) => {
            buf += d;
            const m = buf.match(/DevTools listening on (ws:\S+)/);
            if (m) resolve(m[1]);
        });
        browser.on("exit", () => reject(new Error("chromium exited: " + buf)));
    });

    step = "chromium listening";
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
        window.__csp = [];
        document.addEventListener("securitypolicyviolation", (e) => window.__csp.push(e.violatedDirective + " " + e.blockedURI));
    ` + init });
    const loaded = next("Page.loadEventFired");
    await s("Page.navigate", { url });
    await loaded;
    step = "page loaded";

    const value = async (expr) => (await s("Runtime.evaluate", { expression: expr, returnByValue: true, awaitPromise: true })).result.value;
    const expect = async (what, expr, want) => {
        const got = await value(expr);
        if (JSON.stringify(got) !== JSON.stringify(want)) fail(`${what}: got ${JSON.stringify(got)}, want ${JSON.stringify(want)}`);
        step = what;
    };
    const close = () => { clearTimeout(timer); ws.close(); browser.kill(); };
    return { s, value, expect, fail, close };
}
