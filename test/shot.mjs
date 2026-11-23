// Drive the app in headless Chrome at phone size and take screenshots.
// usage: node test/shot.mjs <outdir> <name>=<hash>[@js-to-run-before-shot] ...
import { spawn } from "node:child_process";
import { writeFileSync, mkdtempSync, mkdirSync } from "node:fs";
import { tmpdir } from "node:os";
import { join } from "node:path";
const [outdir, ...steps] = process.argv.slice(2);
const base = process.env.ROI_URL || "http://127.0.0.1:8787";
const dark = process.env.DARK === "1";
mkdirSync(outdir, { recursive: true });
const port = 9334;
const chrome = spawn("/Applications/Google Chrome.app/Contents/MacOS/Google Chrome", ["--headless=new", "--disable-gpu", "--hide-scrollbars", `--remote-debugging-port=${port}`, `--user-data-dir=${mkdtempSync(join(tmpdir(), "roi-shot-"))}`, "about:blank"], { stdio: "ignore" });
const sleep = (ms) => new Promise((r) => setTimeout(r, ms));
let failed = false;
try {
  let tabs;
  for (let i = 0; i < 40; i++) { try { tabs = await (await fetch(`http://127.0.0.1:${port}/json`)).json(); break; } catch { await sleep(250); } }
  const ws = new WebSocket(tabs.find((t) => t.type === "page").webSocketDebuggerUrl);
  await new Promise((r) => (ws.onopen = r));
  let id = 0; const pending = new Map(); const errors = [];
  ws.onmessage = (e) => { const m = JSON.parse(e.data); if (m.id) pending.get(m.id)?.(m.result ?? m.error); else if (m.method === "Runtime.exceptionThrown") errors.push(m.params.exceptionDetails.exception?.description || m.params.exceptionDetails.text); else if (m.method === "Runtime.consoleAPICalled" && m.params.type === "error") errors.push(m.params.args.map((a) => a.value || a.description).join(" ")); };
  const send = (method, params = {}) => new Promise((r) => { pending.set(++id, r); ws.send(JSON.stringify({ id, method, params })); });
  await send("Runtime.enable"); await send("Page.enable");
  await send("Emulation.setDeviceMetricsOverride", { width: 390, height: 844, deviceScaleFactor: 2, mobile: true });
  if (dark) await send("Emulation.setEmulatedMedia", { features: [{ name: "prefers-color-scheme", value: "dark" }] });
  await send("Page.navigate", { url: base + "/" });
  await sleep(2500);
  for (const step of steps) {
    const [name, rest = ""] = step.split(/=(.*)/s);
    const [hash, js] = rest.split(/@(.*)/s);
    await send("Runtime.evaluate", { expression: `location.hash = ${JSON.stringify(hash)}` });
    await sleep(2200);
    if (js) { const r = await send("Runtime.evaluate", { expression: js, awaitPromise: true, returnByValue: true }); if (r.exceptionDetails) { errors.push(name + ": " + r.exceptionDetails.text); } else if (r.result?.value !== undefined) console.log(name, "→", JSON.stringify(r.result.value)); await sleep(1800); }
    const { data } = await send("Page.captureScreenshot", { format: "png" });
    writeFileSync(join(outdir, name + ".png"), Buffer.from(data, "base64"));
  }
  if (errors.length) { failed = true; console.log("PAGE ERRORS:\n" + errors.join("\n")); } else console.log("no page errors");
} finally { chrome.kill(); }
process.exit(failed ? 1 : 0);
