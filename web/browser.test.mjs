import { test, before, after } from "node:test";
import assert from "node:assert/strict";
import {
  mkdtempSync,
  readFileSync,
  rmSync,
  readdirSync,
  writeFileSync,
} from "node:fs";
import { tmpdir } from "node:os";
import { join, resolve } from "node:path";
import { spawn, execFileSync } from "node:child_process";
import { once } from "node:events";
import { chromium } from "playwright";
let root, processHandle, url, browser;
const errors = [];
before(
  async () => {
    root = mkdtempSync(join(tmpdir(), "songya-ui-browser-"));
    execFileSync(
      "go",
      ["build", "-o", join(root, "songya-web"), "./cmd/songya-web"],
      { cwd: resolve("..") },
    );
    processHandle = spawn(
      join(root, "songya-web"),
      ["--store", join(root, "store"), "--listen", "127.0.0.1:0"],
      { cwd: root },
    );
    let output = "";
    url = await new Promise((done, reject) => {
      processHandle.stdout.on("data", (chunk) => {
        output += chunk;
        const match = output.match(/http:\/\/[^\s]+/);
        if (match) done(match[0]);
      });
      processHandle.on("error", reject);
      processHandle.on("exit", (code) =>
        reject(Error("Server exited " + code)),
      );
    });
    browser = await chromium.launch({ headless: true });
  },
  { timeout: 120000 },
);
after(async () => {
  await browser?.close();
  if (processHandle?.exitCode === null) {
    processHandle.kill("SIGTERM");
    await once(processHandle, "exit");
  }
  if (root) rmSync(root, { recursive: true, force: true });
});
async function pageFor(viewport = { width: 1320, height: 900 }) {
  const context = await browser.newContext({ viewport });
  const requests = [];
  context.on("request", (request) => requests.push(request.url()));
  await context.route("**/*", (route) =>
    route.request().url().startsWith(url) ? route.continue() : route.abort(),
  );
  const page = await context.newPage();
  page.setDefaultTimeout(7000);
  page.on("console", (msg) => {
    if (msg.type() === "error") console.error(msg.text());
  });
  page.on("pageerror", (e) => {
    console.error("PAGEERROR", e.message);
    errors.push(e.message);
  });
  await page.goto(url);
  await page.getByRole("heading", { name: "Library", exact: true }).waitFor();
  return { page, context, requests };
}
async function save(
  source,
  kind = "memories",
  id = "",
  version = "",
  key = crypto.randomUUID(),
) {
  const response = await fetch(url + "/api/records", {
    method: "POST",
    headers: { "Content-Type": "application/json" },
    body: JSON.stringify({ source, kind, id, version, key }),
  });
  const result = await response.json();
  assert.equal(response.status, 200, JSON.stringify(result));
  return result;
}
const source = (title, body = "A remembered afternoon.", extra = "") =>
  `---\nuser: ari\ntitle: ${title}\ncustom:\n  nested: [3, false, yes]\n${extra}---\n${body}`;
async function waitText(page, text) {
  await page.getByText(text, { exact: true }).first().waitFor();
}

test("serves offline bundled editors; creates and visually edits a real memory", async () => {
  const { page, context, requests } = await pageFor();
  await page
    .getByRole("button", { name: "New record", exact: true })
    .first()
    .click();
  await page
    .getByRole("textbox", { name: "Metadata title", exact: true })
    .fill("The blue notebook");
  await page
    .getByRole("textbox", { name: "Metadata user", exact: true })
    .fill("ari");
  await page.locator(".ProseMirror").fill("A quiet afternoon with Alex.");
  await page.getByRole("button", { name: "Save", exact: true }).click();
  await waitText(page, "Saved");
  const files = readdirSync(join(root, "store", "memories")).filter((n) =>
    n.endsWith(".md"),
  );
  assert.equal(files.length, 1);
  assert.match(
    readFileSync(join(root, "store", "memories", files[0]), "utf8"),
    /quiet afternoon/,
  );
  await page.getByRole("tab", { name: "Source", exact: true }).click();
  await page.locator(".cm-content").waitFor();
  assert.ok((await page.locator(".cm-line").count()) > 3);
  assert.match(await page.locator(".cm-content").innerText(), /created_at:/);
  await page.getByRole("tab", { name: "Visual", exact: true }).click();
  assert.match(
    await page.locator(".ProseMirror").innerText(),
    /quiet afternoon/,
  );
  await page.screenshot({
    path: "/tmp/songya-ui-review/desktop.png",
    fullPage: true,
  });
  assert.ok(requests.every((r) => r.startsWith(url)));
  await context.close();
  assert.deepEqual(errors, []);
});

test("all record types, full source, nested metadata and automatic clocks survive the UI", async () => {
  for (const kind of ["memories", "entities", "topics", "stories"]) {
    const record = await save(
      source("Source " + kind, "## A heading\n\nOriginal prose."),
      kind,
    );
    const created = record.draft.fields.find(
      (f) => f.key === "created_at",
    ).value;
    const { page, context } = await pageFor();
    await page
      .getByRole("button")
      .filter({ has: page.getByText("Source " + kind, { exact: true }) })
      .first()
      .click();
    await page.getByRole("tab", { name: "Source", exact: true }).click();
    const content = page.locator(".cm-content");
    await content.waitFor();
    const replacement = record.source
      .replace("Original prose.", "Changed prose.")
      .replace("[3, false, yes]", "[8, false, yes]");
    await content.fill(replacement);
    await page.getByRole("tab", { name: "Visual", exact: true }).click();
    assert.match(
      await page.locator(".ProseMirror").innerText(),
      /Changed prose/,
    );
    await page.getByRole("button", { name: "Save", exact: true }).click();
    await waitText(page, "Saved");
    const doc = await (await fetch(url + "/api/records/" + record.id)).json();
    assert.match(doc.source, /Changed prose/);
    assert.equal(
      doc.draft.fields.find((f) => f.key === "created_at").value,
      created,
    );
    assert.match(doc.source, /custom:/);
    await page.getByRole("button", { name: "Metadata", exact: true }).click();
    assert.equal(
      await page
        .getByRole("textbox", { name: "Metadata created_at", exact: true })
        .isEditable(),
      false,
    );
    await context.close();
  }
  assert.deepEqual(errors, []);
});

test("connections and deletion blockers are navigable; removing the link permits permanent deletion", async () => {
  const entity = await save(source("Alex"), "entities");
  const memory = await save(source("Meeting Alex"));
  const { page, context } = await pageFor();
  await page
    .getByRole("button")
    .filter({ has: page.getByText("Meeting Alex", { exact: true }) })
    .first()
    .click();
  await page.getByRole("button", { name: "Connections", exact: true }).click();
  await page.getByLabel("Find a record", { exact: true }).fill("Alex");
  await page
    .locator(".target-results button")
    .filter({ hasText: entity.id })
    .click();
  await page.getByLabel("Relationship", { exact: true }).fill("with");
  await page
    .getByRole("button", { name: "Add connection", exact: true })
    .click();
  await page.getByRole("button", { name: "Save", exact: true }).click();
  await waitText(page, "Saved");
  await page
    .locator(".edge button")
    .filter({ hasText: "Alex" })
    .first()
    .click();
  await page.getByRole("heading", { name: "Alex", exact: true }).waitFor();
  await page
    .getByRole("button", { name: "Delete permanently", exact: true })
    .click();
  await page.locator(".blockers").waitFor();
  assert.match(await page.locator(".blockers").innerText(), /Meeting Alex/);
  await page.locator(".blockers button").click();
  await page
    .getByRole("heading", { name: "Meeting Alex", exact: true })
    .waitFor();
  if (!(await page.getByLabel("Find a record", { exact: true }).isVisible()))
    await page
      .getByRole("button", { name: "Connections", exact: true })
      .click();
  await page
    .getByRole("button", {
      name: "Remove connection " + entity.id,
      exact: true,
    })
    .click();
  await page.getByRole("button", { name: "Save", exact: true }).click();
  await waitText(page, "Saved");
  await page
    .getByRole("button")
    .filter({ has: page.getByText("Alex", { exact: true }) })
    .first()
    .click();
  await page.getByRole("heading", { name: "Alex", exact: true }).waitFor();
  await page
    .getByRole("button", { name: "Delete permanently", exact: true })
    .click();
  await waitText(page, "Deleted permanently");
  assert.equal((await fetch(url + "/api/records/" + entity.id)).status, 404);
  await context.close();
  assert.deepEqual(errors, []);
});

test("failed acknowledgement keeps the draft and retries the original durable write once", async () => {
  const { page, context } = await pageFor();
  await page
    .getByRole("button", { name: "New record", exact: true })
    .first()
    .click();
  await page
    .getByRole("textbox", { name: "Metadata title", exact: true })
    .fill("Lost reply");
  await page
    .getByRole("textbox", { name: "Metadata user", exact: true })
    .fill("ari");
  await page.locator(".ProseMirror").fill("A precious thought.");
  let first = true;
  await page.route("**/api/records", async (route) => {
    if (route.request().method() === "POST" && first) {
      first = false;
      await route.fetch();
      await route.abort("failed");
    } else await route.continue();
  });
  await page.getByRole("button", { name: "Save", exact: true }).click();
  await page.getByRole("alert").waitFor();
  assert.match(
    await page.locator(".ProseMirror").innerText(),
    /precious thought/,
  );
  await page.getByRole("button", { name: "Save", exact: true }).click();
  await waitText(page, "Saved");
  const records = await (
    await fetch(url + "/api/records?q=Lost%20reply")
  ).json();
  assert.equal(records.records.length, 1);
  await context.close();
  assert.deepEqual(errors, []);
});

test("phone layout, metadata editing, references and unsaved navigation retain the draft", async () => {
  const one = await save(
    source("Phone record", "An ordinary day.\n\n<div>Preserved raw HTML</div>"),
  );
  const two = await save(source("Another phone record"));
  const { page, context } = await pageFor({ width: 390, height: 844 });
  await page
    .getByRole("button")
    .filter({ has: page.getByText("Phone record", { exact: true }) })
    .first()
    .click();
  await page.locator(".ProseMirror p").first().click();
  await page.keyboard.press("End");
  await page.keyboard.type(" A new sentence.");
  await page.getByRole("button", { name: "Library", exact: true }).click();
  await page
    .getByRole("button")
    .filter({ has: page.getByText("Another phone record", { exact: true }) })
    .first()
    .click();
  await page.getByRole("button", { name: "Library", exact: true }).click();
  await page
    .getByRole("button")
    .filter({ has: page.getByText("Phone record", { exact: true }) })
    .first()
    .click();
  await page
    .getByRole("heading", { name: "Phone record", exact: true })
    .waitFor();
  assert.match(await page.locator(".ProseMirror").innerText(), /new sentence/);
  await page.getByRole("tab", { name: "Source", exact: true }).click();
  assert.match(
    await page.locator(".cm-content").innerText(),
    /<div>Preserved raw HTML<\/div>/,
  );
  await page.getByRole("button", { name: "Save", exact: true }).click();
  await waitText(page, "Saved");
  await page.screenshot({
    path: "/tmp/songya-ui-review/phone.png",
    fullPage: true,
  });
  assert.ok(
    await page.evaluate(
      () => document.documentElement.scrollWidth <= innerWidth,
    ),
  );
  await context.close();
  assert.deepEqual(errors, []);
});

test("creates all four types through the visual form and exposes automatic source fields read-only", async () => {
  const { page, context } = await pageFor();
  for (const kind of ["memories", "entities", "topics", "stories"]) {
    await page
      .getByRole("button", { name: "New record", exact: true })
      .first()
      .click();
    await page.getByLabel("Record type", { exact: true }).selectOption(kind);
    await page
      .getByRole("textbox", { name: "Metadata title", exact: true })
      .fill("Created " + kind);
    if (kind === "memories" || kind === "stories")
      await page
        .getByRole("textbox", { name: "Metadata user", exact: true })
        .fill("ari");
    await page.locator(".ProseMirror").fill("Created visually.");
    await page.getByRole("button", { name: "Save", exact: true }).click();
    await waitText(page, "Saved");
    await page
      .getByRole("heading", { name: "Created " + kind, exact: true })
      .waitFor();
  }
  await page.getByRole("tab", { name: "Source", exact: true }).click();
  const content = page.locator(".cm-content");
  await content.waitFor();
  const before = await content.innerText();
  await content.fill(
    before.replace(/created_at: [^\n]+/, "created_at: 1900-01-01"),
  );
  assert.equal(await content.innerText(), before);
  await context.close();
  assert.deepEqual(errors, []);
});

test("reports concurrent edits without overwriting and permits an explicit reload", async () => {
  const record = await save(source("Concurrent record"));
  const { page, context } = await pageFor();
  await page
    .getByRole("button")
    .filter({ has: page.getByText("Concurrent record", { exact: true }) })
    .first()
    .click();
  await page
    .getByRole("heading", { name: "Concurrent record", exact: true })
    .waitFor();
  await page.locator(".ProseMirror").fill("My unsaved draft.");
  await save(
    record.source.replace("A remembered afternoon.", "An external change."),
    "memories",
    record.id,
    record.version,
  );
  await page.getByRole("button", { name: "Save", exact: true }).click();
  await page.getByRole("alert").waitFor();
  assert.match(
    await page.locator(".ProseMirror").innerText(),
    /My unsaved draft/,
  );
  const disk = await (await fetch(url + "/api/records/" + record.id)).json();
  assert.match(disk.source, /external change/);
  page.once("dialog", (dialog) => dialog.accept());
  await page
    .getByRole("button", { name: "Load saved version", exact: true })
    .click();
  await page
    .getByRole("heading", { name: "Concurrent record", exact: true })
    .waitFor();
  await page.waitForFunction(() =>
    document
      .querySelector(".ProseMirror")
      ?.textContent.includes("external change"),
  );
  await context.close();
  assert.deepEqual(errors, []);
});

test("source and visual metadata share one draft, including new nested metadata", async () => {
  const record = await save(source("Shared draft"));
  const { page, context } = await pageFor();
  await page
    .getByRole("button")
    .filter({ has: page.getByText("Shared draft", { exact: true }) })
    .first()
    .click();
  await page
    .getByRole("heading", { name: "Shared draft", exact: true })
    .waitFor();
  await page.getByRole("tab", { name: "Source", exact: true }).click();
  const code = page.getByRole("textbox", {
    name: "Markdown source",
    exact: true,
  });
  await code.waitFor();
  await code.fill(
    record.source.replace("A remembered afternoon.", "Changed in source."),
  );
  await page.getByRole("button", { name: "Metadata", exact: true }).click();
  await page
    .getByRole("textbox", { name: "Metadata title", exact: true })
    .fill("Shared edited draft");
  await page.getByRole("button", { name: "Add metadata", exact: true }).click();
  await page
    .getByLabel("Metadata name", { exact: true })
    .last()
    .fill("new_nested");
  await page
    .getByRole("combobox", { name: "Value type new_nested", exact: true })
    .selectOption("yaml");
  await page
    .locator('textarea[aria-label="Metadata new_nested"]')
    .fill("items: [3, true]\nlocation: {name: Garden}");
  await page.getByRole("button", { name: "Save", exact: true }).click();
  await waitText(page, "Saved");
  const saved = await (await fetch(url + "/api/records/" + record.id)).json();
  assert.match(saved.source, /Changed in source/);
  assert.match(saved.source, /Shared edited draft/);
  assert.match(saved.source, /new_nested:/);
  assert.match(saved.source, /Garden/);
  await context.close();
  assert.deepEqual(errors, []);
});

test("a malformed draft is retained while healthy records stay editable", async () => {
  const bad = await save(source("Draft with a mistake"));
  const healthy = await save(source("Healthy neighbour"));
  const { page, context } = await pageFor();
  await page
    .getByRole("button")
    .filter({ has: page.getByText("Draft with a mistake", { exact: true }) })
    .first()
    .click();
  await page
    .getByRole("heading", { name: "Draft with a mistake", exact: true })
    .waitFor();
  await page.getByRole("button", { name: "Metadata", exact: true }).click();
  await page
    .locator('textarea[aria-label="Metadata custom"]')
    .fill("broken: [unfinished");
  await page.getByRole("button", { name: "Save", exact: true }).click();
  await page.getByRole("alert").waitFor();
  await page
    .getByRole("button")
    .filter({ has: page.getByText("Healthy neighbour", { exact: true }) })
    .first()
    .click();
  await page
    .getByRole("heading", { name: "Healthy neighbour", exact: true })
    .waitFor();
  await page.locator(".ProseMirror").fill("Still editable.");
  await page.getByRole("button", { name: "Save", exact: true }).click();
  await waitText(page, "Saved");
  await page
    .getByRole("button")
    .filter({ has: page.getByText("Draft with a mistake", { exact: true }) })
    .first()
    .click();
  await page
    .getByRole("heading", { name: "Draft with a mistake", exact: true })
    .waitFor();
  if (
    !(await page.locator('textarea[aria-label="Metadata custom"]').isVisible())
  )
    await page.getByRole("button", { name: "Metadata", exact: true }).click();
  assert.equal(
    await page.locator('textarea[aria-label="Metadata custom"]').inputValue(),
    "broken: [unfinished",
  );
  await page
    .locator('textarea[aria-label="Metadata custom"]')
    .fill("fixed: [3, false]");
  await page.getByRole("button", { name: "Save", exact: true }).click();
  await waitText(page, "Saved");
  assert.match(
    (await (await fetch(url + "/api/records/" + bad.id)).json()).source,
    /fixed:/,
  );
  await context.close();
  assert.deepEqual(errors, []);
});

test("quoted automatic timestamps are protected and malformed records leave healthy reads usable", async () => {
  const id = "m_000000000099";
  writeFileSync(
    join(root, "store", "memories", id + ".md"),
    '---\r\nuser: ari\r\ntitle: Quoted clocks\r\n"created_at": 2024-01-01T00:00:00Z\r\n"updated_at": 2024-01-01T00:00:00Z\r\n---\r\nA record.',
  );
  writeFileSync(
    join(root, "store", "memories", "m_000000000098.md"),
    "---\ntitle: [broken\n---\nRetained prose.",
  );
  const { page, context } = await pageFor();
  await page
    .getByRole("button")
    .filter({ has: page.getByText("Quoted clocks", { exact: true }) })
    .first()
    .click();
  await page
    .getByRole("heading", { name: "Quoted clocks", exact: true })
    .waitFor();
  await page.getByRole("tab", { name: "Source", exact: true }).click();
  const editor = page.getByRole("textbox", {
    name: "Markdown source",
    exact: true,
  });
  await editor.waitFor();
  const original = await editor.innerText();
  await editor.fill(
    original.replace("2024-01-01T00:00:00Z", "1900-01-01T00:00:00Z"),
  );
  assert.equal(await editor.innerText(), original);
  await editor.fill(original.replace("A record.", "A healthy edit."));
  await page.getByRole("button", { name: "Save", exact: true }).click();
  await waitText(page, "Saved");
  assert.match(
    (await (await fetch(url + "/api/records/" + id)).json()).source,
    /healthy edit/,
  );
  await context.close();
  assert.deepEqual(errors, []);
});

test("owner and type filters preserve shared identities without adding access restrictions", async () => {
  await save(source("Ownership Bob"), "memories").then(async (record) => {
    await save(
      record.source.replace("user: ari", "user: bob"),
      "memories",
      record.id,
      record.version,
    );
  });
  await save(source("Ownership Ari"), "memories");
  await save(source("Ownership shared"), "entities");
  const { page, context } = await pageFor();
  await page
    .getByRole("searchbox", { name: "Search records", exact: true })
    .fill("Ownership");
  await page.getByLabel("Owner", { exact: true }).selectOption("bob");
  await page.waitForFunction(
    () => document.querySelectorAll(".record").length === 2,
  );
  const rows = await page.locator(".record").allTextContents();
  assert.ok(rows.some((r) => r.includes("Ownership Bob")));
  assert.ok(rows.some((r) => r.includes("Ownership shared")));
  assert.ok(!rows.some((r) => r.includes("Ownership Ari")));
  await page.getByLabel("Type", { exact: true }).selectOption("memories");
  await page.waitForFunction(
    () => document.querySelectorAll(".record").length === 1,
  );
  assert.match(await page.locator(".record").innerText(), /Ownership Bob/);
  await context.close();
  assert.deepEqual(errors, []);
});

test("edits after a lost reply resolve the original write before saving the new draft", async () => {
  const { page, context } = await pageFor();
  await page
    .getByRole("button", { name: "New record", exact: true })
    .first()
    .click();
  await page
    .getByRole("textbox", { name: "Metadata title", exact: true })
    .fill("A changing draft");
  await page
    .getByRole("textbox", { name: "Metadata user", exact: true })
    .fill("ari");
  await page
    .getByRole("textbox", { name: "Record text", exact: true })
    .fill("Original thought.");
  let first = true;
  await page.route("**/api/records", async (route) => {
    if (route.request().method() === "POST" && first) {
      first = false;
      await route.fetch();
      await route.abort("failed");
    } else await route.continue();
  });
  await page.getByRole("button", { name: "Save", exact: true }).click();
  await page.getByRole("alert").waitFor();
  await page
    .getByRole("textbox", { name: "Record text", exact: true })
    .fill("A newer thought.");
  await page.getByRole("button", { name: "Save", exact: true }).click();
  await waitText(
    page,
    "Previous save confirmed. Your newer changes are still unsaved.",
  );
  assert.match(
    await page
      .getByRole("textbox", { name: "Record text", exact: true })
      .innerText(),
    /newer thought/,
  );
  await page.getByRole("button", { name: "Save", exact: true }).click();
  await waitText(page, "Saved");
  const result = await (
    await fetch(url + "/api/records?q=A%20changing%20draft")
  ).json();
  const matching = result.records.filter((r) => r.title === "A changing draft");
  assert.equal(matching.length, 1);
  assert.match(matching[0].preview, /newer thought/);
  await context.close();
  assert.deepEqual(errors, []);
});

test("discarding a new draft creates no record", async () => {
  const before = await (await fetch(url + "/api/records")).json();
  const { page, context } = await pageFor();
  await page
    .getByRole("button", { name: "New record", exact: true })
    .first()
    .click();
  await page
    .getByRole("textbox", { name: "Metadata title", exact: true })
    .fill("Never saved");
  page.once("dialog", (d) => d.accept());
  await page
    .getByRole("button", { name: "Discard draft", exact: true })
    .click();
  await waitText(page, "Draft discarded");
  const after = await (await fetch(url + "/api/records")).json();
  assert.equal(after.records.length, before.records.length);
  await context.close();
  assert.deepEqual(errors, []);
});

test("a rejected save can be corrected and saved without retaining the invalid request", async () => {
  const { page, context } = await pageFor();
  await page
    .getByRole("button", { name: "New record", exact: true })
    .first()
    .click();
  await page
    .getByRole("textbox", { name: "Metadata title", exact: true })
    .fill("Corrected owner");
  await page.locator(".ProseMirror").fill("A draft without its owner yet.");
  await page.getByRole("button", { name: "Save", exact: true }).click();
  await page.getByRole("alert").waitFor();
  await page
    .getByRole("textbox", { name: "Metadata user", exact: true })
    .fill("ari");
  await page.getByRole("button", { name: "Save", exact: true }).click();
  await waitText(page, "Saved");
  const listed = await (
    await fetch(url + "/api/records?q=Corrected%20owner")
  ).json();
  assert.equal(
    listed.records.filter((r) => r.title === "Corrected owner").length,
    1,
  );
  await context.close();
  assert.deepEqual(errors, []);
});
