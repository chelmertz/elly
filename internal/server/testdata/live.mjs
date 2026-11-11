// Checks the page's live swap against elly's real handler and change
// stream. Run by TestPageLive: node live.mjs <chromium> <url>. Bury goes
// through the real endpoint; the row must change without a reload while
// the open row, the focused row and the filter stay as they were.
import { openPage } from "./cdp.mjs";

const [chromium, url] = process.argv.slice(2);
const { value, expect, fail, close } = await openPage(chromium, url, `
    window.__marker = "kept";
    // the clock can be moved forward, to see the 20 s start wait run out
    window.__skew = 0;
    const now = Date.now;
    Date.now = () => now() + window.__skew;
    // a claude-review link would leave for the OS handler
    document.addEventListener("click", (e) => {
        const a = e.target instanceof Element && e.target.closest("a[href^='claude-review:']");
        if (a) e.preventDefault();
    }, true);
    window.__events = 0;
    const Source = window.EventSource;
    window.EventSource = function (u) {
        const e = new Source(u);
        e.addEventListener("message", () => { window.__events++; });
        return e;
    };
`);
const waitFor = async (what, expr) => {
    for (let i = 0; i < 50; i++) {
        if (await value(expr)) return true;
        await new Promise((r) => setTimeout(r, 100));
    }
    fail(what + ": timed out");
    return false;
};

await waitFor("the stream connects", "window.__events >= 1");
await value("document.querySelector('#pr-o-r-2 .toggle').click()");
await value("document.querySelector('#pr-o-r-1 .toggle').focus()");
await value("const f = document.getElementById('filter'); f.value = '#1'; f.dispatchEvent(new Event('input'))");
const shown = "Array.from(document.querySelectorAll('.pr'), (el) => el.id + ':' + el.hidden).sort().join(',')";
const hiddenBefore = await value(shown);
if (!hiddenBefore.includes("pr-o-r-1:false") || !hiddenBefore.includes(":true")) fail("the filter should keep #1 and hide others: " + hiddenBefore);

const verb6 = "document.querySelector('#pr-o-r-6 a.verb')";
await expect("the offer is the row's verb", verb6 + ".textContent", "Start");
await value(verb6 + ".click()");
await expect("a clicked Start says starting at once", verb6 + ".textContent", "starting…");

await value("document.querySelector('#pr-o-r-3 button.bury').click()");
if (await waitFor("the buried row re-renders", "document.querySelector('#pr-o-r-3 button.bury').textContent === 'Unbury'")) {
    await expect("no reload", "window.__marker", "kept");
    await expect("the open row stays open", "document.getElementById('pr-o-r-2').classList.contains('open')", true);
    await expect("its panel stays shown", "document.querySelector('#pr-o-r-2 .panel').hidden", false);
    await expect("the focused row keeps focus", "document.activeElement.closest('.pr') && document.activeElement.closest('.pr').id", "pr-o-r-1");
    await expect("the filter is kept", "document.getElementById('filter').value", "#1");
    await expect("and still applied", shown, hiddenBefore);
}
await expect("starting survives a swap", verb6 + ".textContent", "starting…");
await value("window.__skew = 21000; document.querySelector('#pr-o-r-3 button.bury').click()");
await waitFor("an unanswered Start says so", verb6 + ".textContent === 'no answer'");
await expect("and can be clicked again", verb6 + ".classList.contains('starting')", false);
await expect("the CSP blocked nothing", "window.__csp", []);
close();
