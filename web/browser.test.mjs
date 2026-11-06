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
async function pageFor(viewport = { width: 1320, height: 900 }, init) {
  const context = await browser.newContext({ viewport });
  if (init) await context.addInitScript(init);
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
  await page
    .locator(".messages:visible")
    .getByText(text, { exact: true })
    .waitFor();
}

async function editRecord(page, title) {
  if (await page.locator("dialog[open]").count())
    await page
      .getByRole("button", { name: "Close record", exact: true })
      .click();
  await page.locator("dialog").waitFor({ state: "hidden" });
  await page
    .getByRole("button", { name: "Edit " + title, exact: true })
    .click();
  await page.getByRole("dialog").waitFor();
}

// Locate a rendered sphere in a screenshot, rather than exposing renderer internals.
async function graphPoint(page, color) {
  const canvas = page.locator(".graph-canvas canvas");
  const png = (await canvas.screenshot()).toString("base64");
  return page.evaluate(
    async ({ png, color }) => {
      const image = new Image();
      image.src = "data:image/png;base64," + png;
      await image.decode();
      const canvas = document.createElement("canvas");
      canvas.width = image.width;
      canvas.height = image.height;
      const context = canvas.getContext("2d");
      context.drawImage(image, 0, 0);
      const pixels = context.getImageData(0, 0, image.width, image.height).data;
      let sumX = 0,
        sumY = 0,
        count = 0;
      for (let y = 15; y < image.height - 45; y++) {
        for (let x = 15; x < image.width - 15; x++) {
          const i = (y * image.width + x) * 4;
          const [r, g, b] = pixels.slice(i, i + 3);
          const score =
            color === "green"
              ? g - Math.max(r, b)
              : color === "red"
                ? r - Math.max(g, b)
                : color === "amber"
                  ? Math.min(r, g) - b
                  : Math.min(r, g, b) - Math.max(r, g, b) * 0.5;
          if (score > 50) {
            sumX += x;
            sumY += y;
            count++;
          }
        }
      }
      return count ? { x: sumX / count, y: sumY / count } : null;
    },
    { png, color },
  );
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
    await editRecord(page, "Source " + kind);
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
  await editRecord(page, "Meeting Alex");
  await page.getByRole("button", { name: "Connections", exact: true }).click();
  await page.getByLabel("Find a record", { exact: true }).fill("Alex");
  await page
    .locator(".target-results button")
    .filter({ hasText: entity.id })
    .click();
  await page
    .getByLabel("Relationship", { exact: true })
    .selectOption("related_to");
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
  await editRecord(page, "Alex");
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
  await editRecord(page, "Phone record");
  await page.locator(".ProseMirror p").first().click();
  await page.keyboard.press("End");
  await page.keyboard.type(" A new sentence.");
  await editRecord(page, "Another phone record");
  await editRecord(page, "Phone record");
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
    if (await page.locator("dialog[open]").count())
      await page
        .getByRole("button", { name: "Close record", exact: true })
        .click();
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
  await editRecord(page, "Concurrent record");
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
  await editRecord(page, "Shared draft");
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
  await editRecord(page, "Draft with a mistake");
  await page
    .getByRole("heading", { name: "Draft with a mistake", exact: true })
    .waitFor();
  await page.getByRole("button", { name: "Metadata", exact: true }).click();
  await page
    .locator('textarea[aria-label="Metadata custom"]')
    .fill("broken: [unfinished");
  await page.getByRole("button", { name: "Save", exact: true }).click();
  await page.getByRole("alert").waitFor();
  await editRecord(page, "Healthy neighbour");
  await page
    .getByRole("heading", { name: "Healthy neighbour", exact: true })
    .waitFor();
  await page.locator(".ProseMirror").fill("Still editable.");
  await page.getByRole("button", { name: "Save", exact: true }).click();
  await waitText(page, "Saved");
  await editRecord(page, "Draft with a mistake");
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

test("quoted automatic timestamps are protected and malformed records leave healthy reads usable", async (t) => {
  const damagedPath = join(root, "store", "memories", "m_000000000098.md");
  t.after(() => rmSync(damagedPath, { force: true }));
  const id = "m_000000000099";
  writeFileSync(
    join(root, "store", "memories", id + ".md"),
    '---\r\nuser: ari\r\ntitle: Quoted clocks\r\n"created_at": 2024-01-01T00:00:00Z\r\n"updated_at": 2024-01-01T00:00:00Z\r\n---\r\nA record.',
  );
  writeFileSync(damagedPath, "---\ntitle: [broken\n---\nRetained prose.");
  const { page, context } = await pageFor();
  await editRecord(page, "Quoted clocks");
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
  await page
    .getByLabel("Record type filter", { exact: true })
    .selectOption("memories");
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
  const saved = await (
    await fetch(url + "/api/records/" + matching[0].id)
  ).json();
  assert.match(saved.source, /newer thought/);
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

test("the front page is a sortable metadata table with independent column filters and read-only inspection", async () => {
  for (const [title, description, type, alias, kind] of [
    ["Table 2", "Cedar tree", "plant taxon", "Atlas", "entities"],
    ["Table 10", "Botanist", "person", "Zoe", "entities"],
    ["Table 1", "A walk outdoors", "experience", "Cedar walk", "memories"],
  ])
    await save(
      source(
        title,
        kind === "memories" ? "# Inspectable prose" : description,
        `${kind === "memories" ? `description: ${description}\n` : ""}type: ${type}\naliases: [${alias}]\n`,
      ),
      kind,
    );
  const { page, context } = await pageFor();
  await page.getByLabel("Filter title", { exact: true }).fill("Table ");
  assert.equal(await page.locator(".record").count(), 3);
  assert.equal(await page.locator("aside").count(), 0);
  assert.equal(await page.locator("dialog[open]").count(), 0);
  const titles = () =>
    page.locator(".title-cell .text-button").allTextContents();
  assert.deepEqual(await titles(), ["Table 1", "Table 2", "Table 10"]);
  await page
    .getByRole("button", { name: "Sort by Title", exact: true })
    .click();
  assert.deepEqual(await titles(), ["Table 10", "Table 2", "Table 1"]);
  assert.equal(
    await page.getByRole("columnheader").first().getAttribute("aria-sort"),
    "descending",
  );
  for (const column of [
    "Description",
    "Record type",
    "Type",
    "Aliases",
    "Owner",
  ]) {
    await page
      .getByRole("button", { name: "Sort by " + column, exact: true })
      .click();
    const header = page.getByRole("columnheader").filter({
      has: page.getByRole("button", {
        name: "Sort by " + column,
        exact: true,
      }),
    });
    assert.equal(await header.getAttribute("aria-sort"), "ascending");
    await page
      .getByRole("button", { name: "Sort by " + column, exact: true })
      .click();
    assert.equal(await header.getAttribute("aria-sort"), "descending");
  }
  await page.getByLabel("Filter description", { exact: true }).fill("cedar");
  assert.deepEqual(await titles(), ["Table 2"]);
  await page.getByLabel("Filter description", { exact: true }).fill("");
  await page.getByLabel("Filter aliases", { exact: true }).fill("zoe");
  assert.deepEqual(await titles(), ["Table 10"]);
  await page.getByLabel("Filter aliases", { exact: true }).fill("");
  await page
    .getByLabel("Metadata type filter", { exact: true })
    .selectOption("plant taxon");
  assert.deepEqual(await titles(), ["Table 2"]);
  await page.getByRole("button", { name: "Table 2", exact: true }).click();
  const modal = page.getByRole("dialog");
  await modal.waitFor();
  assert.equal(
    await modal.locator(".ProseMirror").getAttribute("contenteditable"),
    "false",
  );
  assert.match(
    await modal.locator(".inspection-metadata").innerText(),
    /plant taxon/,
  );
  await page.getByRole("tab", { name: "Source", exact: true }).click();
  assert.equal(
    await modal.locator(".cm-content").getAttribute("contenteditable"),
    "false",
  );
  await modal.getByRole("button", { name: "Edit", exact: true }).click();
  assert.equal(
    await modal.locator(".cm-content").getAttribute("contenteditable"),
    "true",
  );
  await page.keyboard.press("Escape");
  await modal.waitFor({ state: "hidden" });
  assert.deepEqual(await titles(), ["Table 2"]);
  await page.getByRole("button", { name: "Edit Table 2", exact: true }).click();
  await modal.waitFor();
  assert.equal(
    await modal.locator(".ProseMirror").getAttribute("contenteditable"),
    "true",
  );
  await page.screenshot({
    path: "/tmp/songya-ui-review/table-modal.png",
    fullPage: true,
  });
  await page.keyboard.press("Escape");
  await modal.waitFor({ state: "hidden" });
  await page
    .getByLabel("Metadata type filter", { exact: true })
    .selectOption("");
  await page.screenshot({
    path: "/tmp/songya-ui-review/table.png",
    fullPage: true,
  });
  await page.setViewportSize({ width: 390, height: 844 });
  assert.ok(
    await page.evaluate(
      () => document.documentElement.scrollWidth <= innerWidth,
    ),
  );
  assert.ok(
    await page
      .locator(".table-scroll")
      .evaluate((el) => el.scrollWidth > el.clientWidth),
  );
  await page.screenshot({
    path: "/tmp/songya-ui-review/table-phone.png",
    fullPage: true,
  });
  await page.getByRole("button", { name: "Edit Table 2", exact: true }).click();
  await modal.waitFor();
  const bounds = await modal.boundingBox();
  assert.ok(bounds.x >= 0 && bounds.x + bounds.width <= 390);
  await page.getByRole("button", { name: "Close record", exact: true }).click();
  await modal.waitFor({ state: "hidden" });
  await context.close();
  assert.deepEqual(errors, []);
});

test("table deletion shows navigable blockers and removes only an unreferenced record", async () => {
  const target = await save(source("Table deletion target"), "entities");
  const incoming = await save(
    source(
      "Table deletion source",
      "A linked event.",
      `relations: [{type: related_to, target: ${target.id}}]\n`,
    ),
  );
  const { page, context } = await pageFor();
  await page
    .getByRole("button", { name: "Delete Table deletion target", exact: true })
    .click();
  await page.getByRole("dialog").waitFor();
  await page
    .locator(".blockers button")
    .filter({ hasText: "Table deletion source" })
    .click();
  await page
    .getByRole("heading", { name: "Table deletion source", exact: true })
    .waitFor();
  await page.getByRole("button", { name: "Connections", exact: true }).click();
  await page
    .getByRole("button", {
      name: "Remove connection " + target.id,
      exact: true,
    })
    .click();
  await page.getByRole("button", { name: "Save", exact: true }).click();
  await waitText(page, "Saved");
  await page.getByRole("button", { name: "Close record", exact: true }).click();
  await page.getByRole("dialog").waitFor({ state: "hidden" });
  await page
    .getByRole("button", { name: "Delete Table deletion target", exact: true })
    .click();
  await waitText(page, "Deleted permanently");
  assert.equal((await fetch(url + "/api/records/" + target.id)).status, 404);
  assert.equal((await fetch(url + "/api/records/" + incoming.id)).status, 200);
  await context.close();
  assert.deepEqual(errors, []);
});

test("backdrop and Escape close the modal while keeping drafts locally", async () => {
  const record = await save(source("Modal dismissal"));
  const { page, context } = await pageFor();
  await editRecord(page, "Modal dismissal");
  const modal = page.getByRole("dialog");
  await page.locator(".ProseMirror").fill("An unsaved local draft.");
  // Closing must not depend on composing the draft on the server.
  await page.route("**/api/draft", (route) => route.abort());
  await modal
    .getByRole("heading", { name: "Modal dismissal", exact: true })
    .click();
  assert.equal(await modal.isVisible(), true);
  await page.mouse.click(4, 4);
  await modal.waitFor({ state: "hidden" });
  await editRecord(page, "Modal dismissal");
  assert.match(
    await page.locator(".ProseMirror").innerText(),
    /unsaved local draft/,
  );
  await page.unroute("**/api/draft");
  await page.getByRole("tab", { name: "Source", exact: true }).click();
  await page.locator(".cm-content").focus();
  await page.keyboard.press("Escape");
  await modal.waitFor({ state: "hidden" });
  await editRecord(page, "Modal dismissal");
  assert.match(
    await page.locator(".cm-content").innerText(),
    /unsaved local draft/,
  );
  await page.getByRole("tab", { name: "Visual", exact: true }).click();
  await page.locator(".ProseMirror").focus();
  await page.keyboard.press("Escape");
  await modal.waitFor({ state: "hidden" });
  await page
    .getByRole("button", { name: "Modal dismissal", exact: true })
    .click();
  await modal.waitFor();
  await page.mouse.click(4, 4);
  await modal.waitFor({ state: "hidden" });
  const stored = await (await fetch(url + "/api/records/" + record.id)).json();
  assert.equal(stored.source, record.source);
  await context.close();
  assert.deepEqual(errors, []);
});

test("graph views render, preserve search when switching, and open safe hover labels in the inspector", async () => {
  const title = "Graph <b>Alex</b>";
  const entity = await save(
    source(title, "", "aliases: [GraphAlias]\n"),
    "entities",
  );
  const topic = await save(source("Graph topic"), "topics");
  const memory = await save(
    source(
      "Graph memory",
      "A connected afternoon.",
      `relations: [{type: related_to, target: ${entity.id}}, {type: related_to, target: ${topic.id}}]\n`,
    ),
  );
  const story = await save(
    source(
      "Graph story",
      "",
      `relations: [{type: related_to, target: ${memory.id}}]\n`,
    ),
    "stories",
  );
  const ids = new Set([entity.id, topic.id, memory.id, story.id]);
  const data = await (await fetch(url + "/api/graph")).json();
  data.nodes = data.nodes.filter((node) => ids.has(node.id));
  data.links = data.links.filter((link) => ids.has(link.source));
  data.links.push({ source: memory.id, target: "m_missing", type: "about" });
  const { page, context } = await pageFor();
  await page.route("**/api/graph", (route) => route.fulfill({ json: data }));
  await page.reload();
  await page.locator(".graph-canvas canvas").waitFor();
  await page.waitForTimeout(3000); // Let the force layout settle and the camera fit.
  const graph = page.getByRole("region", { name: "Memory graph", exact: true });
  const bounds = await graph.boundingBox();
  assert.equal(bounds.height, 450);
  const views = page.getByRole("group", { name: "Graph view", exact: true });
  assert.equal(
    await views
      .getByRole("button", { name: "2D", exact: true })
      .getAttribute("aria-pressed"),
    "true",
  );
  const expand = page.getByRole("button", {
    name: "Expand graph to full screen",
    exact: true,
  });
  await expand.click();
  await page.waitForFunction(
    () =>
      document.querySelector(".graph-canvas canvas").getBoundingClientRect()
        .height === innerHeight,
  );
  assert.deepEqual(await graph.boundingBox(), {
    x: 0,
    y: 0,
    width: 1320,
    height: 900,
  });
  assert.equal(await page.evaluate(() => document.fullscreenElement), null);
  assert.equal(await page.locator("main").evaluate((el) => el.inert), true);
  await page.keyboard.press("Escape");
  await page.waitForFunction(
    () =>
      document.querySelector(".graph-canvas canvas").getBoundingClientRect()
        .height === 450,
  );
  assert.equal(await page.locator("main").evaluate((el) => el.inert), false);
  await expand.click();
  await page.waitForFunction(
    () =>
      document.querySelector(".graph-canvas canvas").getBoundingClientRect()
        .height === innerHeight,
  );
  await page.waitForTimeout(500); // Let resizing update both drawing and hit testing.
  const expandedPoint = await graphPoint(page, "green");
  await page.mouse.move(expandedPoint.x, expandedPoint.y);
  await page
    .locator(".float-tooltip-kap")
    .getByText(title, { exact: true })
    .waitFor();
  await page.mouse.click(expandedPoint.x, expandedPoint.y);
  await page
    .getByRole("dialog")
    .getByRole("heading", { name: title, exact: true })
    .waitFor();
  await page.keyboard.press("Escape");
  await page.getByRole("dialog").waitFor({ state: "hidden" });
  assert.equal(
    await page
      .getByRole("button", { name: "Exit full-screen graph", exact: true })
      .isVisible(),
    true,
  );
  await page
    .getByRole("button", { name: "Exit full-screen graph", exact: true })
    .click();
  await page.waitForTimeout(500);
  assert.equal(
    await graph.evaluate((el) => getComputedStyle(el).backgroundColor),
    "rgb(0, 0, 0)",
  );
  for (const color of ["green", "amber", "white", "red"]) {
    assert.ok(
      await graphPoint(page, color),
      "Missing rendered " + color + " sphere",
    );
  }
  const point = await graphPoint(page, "green");
  const canvas = await page.locator(".graph-canvas canvas").boundingBox();
  await page.mouse.move(canvas.x + point.x, canvas.y + point.y);
  const tooltip = page.locator(".float-tooltip-kap");
  await tooltip.getByText(title, { exact: true }).waitFor();
  assert.equal(await tooltip.locator("b").count(), 0);
  await page.mouse.click(canvas.x + point.x, canvas.y + point.y);
  const modal = page.getByRole("dialog");
  await modal.getByRole("heading", { name: title, exact: true }).waitFor();
  assert.equal(
    await modal.locator(".ProseMirror").getAttribute("contenteditable"),
    "false",
  );
  await page.keyboard.press("Escape");
  await modal.waitFor({ state: "hidden" });
  const search = page.getByRole("searchbox", {
    name: "Search graph",
    exact: true,
  });
  const results = page.locator("#graph-matches");
  const rowCount = await page.locator("tbody tr.record").count();
  await search.fill("gRaPhAlIaS");
  await results
    .getByRole("button", { name: title + " entities", exact: true })
    .waitFor();
  assert.equal(await results.locator("button").count(), 1);
  assert.equal(await page.locator("tbody tr.record").count(), rowCount);
  await search.press("Enter");
  await results.waitFor({ state: "hidden" });
  await page.waitForTimeout(750); // Camera transition finishes at 650ms.
  const focused = await graphPoint(page, "green");
  assert.ok(Math.abs(focused.x - canvas.width / 2) < 15);
  assert.ok(Math.abs(focused.y - canvas.height / 2) < 15);
  assert.equal(await graphPoint(page, "red"), null);
  await page.mouse.move(canvas.x + focused.x, canvas.y + focused.y);
  await tooltip.getByText(title, { exact: true }).waitFor();
  await page.mouse.click(canvas.x + focused.x, canvas.y + focused.y);
  await modal.getByRole("heading", { name: title, exact: true }).waitFor();
  await page.keyboard.press("Escape");
  await modal.waitFor({ state: "hidden" });
  await search.fill("no-such-record");
  await results.getByText("No matches", { exact: true }).waitFor();
  await search.fill(entity.id);
  await results
    .getByRole("button", { name: title + " entities", exact: true })
    .click();
  await results.waitFor({ state: "hidden" });
  await search.focus();
  await search.press("Escape");
  assert.equal(await search.inputValue(), "");
  await page.waitForTimeout(600);
  for (const color of ["green", "amber", "white", "red"]) {
    assert.ok(await graphPoint(page, color), "Clear did not restore " + color);
  }
  await search.fill("GraphAlias");
  for (const mode of ["2D", "3D"]) {
    await views.getByRole("button", { name: mode, exact: true }).click();
    assert.equal(
      await views
        .getByRole("button", { name: mode, exact: true })
        .getAttribute("aria-pressed"),
      "true",
    );
    assert.equal(await search.inputValue(), "GraphAlias");
    assert.equal(await page.locator(".graph-canvas canvas").count(), 1);
    assert.equal(
      await page
        .locator(".graph-canvas canvas")
        .evaluate(
          (canvas, mode) =>
            !!canvas.getContext(mode === "2D" ? "2d" : "webgl2"),
          mode,
        ),
      true,
    );
    await page.waitForTimeout(3000);
    await search.focus();
    await search.press("Enter");
    await page.waitForTimeout(750);
    const point = await graphPoint(page, "green");
    assert.ok(
      Math.abs(point.x - canvas.width / 2) < 15,
      mode + " search did not center x",
    );
    assert.ok(
      Math.abs(point.y - canvas.height / 2) < 15,
      mode + " search did not center y",
    );
    assert.equal(await graphPoint(page, "red"), null);
    await page.mouse.move(canvas.x + point.x, canvas.y + point.y);
    await tooltip.getByText(title, { exact: true }).waitFor();
    assert.equal(await tooltip.locator("b").count(), 0);
    await page.mouse.click(canvas.x + point.x, canvas.y + point.y);
    await modal.getByRole("heading", { name: title, exact: true }).waitFor();
    await page.keyboard.press("Escape");
    await modal.waitFor({ state: "hidden" });
    if (mode === "2D") {
      await page.setViewportSize({ width: 390, height: 844 });
      await page.waitForFunction(
        () =>
          document.querySelector(".graph-canvas canvas").getBoundingClientRect()
            .width === 390,
      );
      const input = await search.boundingBox();
      const toggle = await views.boundingBox();
      assert.ok(input.x + input.width <= toggle.x);
      await expand.click();
      await page.waitForFunction(
        () =>
          document.querySelector(".graph-canvas canvas").getBoundingClientRect()
            .height === innerHeight,
      );
      assert.deepEqual(await graph.boundingBox(), {
        x: 0,
        y: 0,
        width: 390,
        height: 844,
      });
      assert.equal(await search.inputValue(), "GraphAlias");
      const controls = await page.locator(".graph-tools").boundingBox();
      assert.ok(controls.x >= 0 && controls.x + controls.width <= 390);
      await page.screenshot({
        path: "/tmp/songya-ui-review/graph-expanded-phone.png",
        fullPage: true,
      });
      await page.keyboard.press("Escape");
      await page.waitForFunction(
        () =>
          document.querySelector(".graph-canvas canvas").getBoundingClientRect()
            .height === 422,
      );
      await page.screenshot({
        path: "/tmp/songya-ui-review/graph-2d-phone.png",
        fullPage: true,
      });
      await page.setViewportSize({ width: 1320, height: 900 });
    }
  }
  await search.focus();
  await search.press("Escape");
  await page.setViewportSize({ width: 390, height: 844 });
  await page.waitForFunction(
    () =>
      document.querySelector(".graph-canvas canvas").getBoundingClientRect()
        .width === 390,
  );
  assert.equal((await graph.boundingBox()).height, 422);
  await page.screenshot({
    path: "/tmp/songya-ui-review/graph-phone.png",
    fullPage: true,
  });
  await page.setViewportSize({ width: 1320, height: 900 });
  await page.screenshot({
    path: "/tmp/songya-ui-review/graph.png",
    fullPage: true,
  });
  await context.close();
  assert.deepEqual(errors, []);
});

test("dense 2D graphs render separate disks for every node", async () => {
  const nodes = Array.from({ length: 90 }, (_, i) => ({
    id: "e_dense_" + i,
    title: "Dense node " + i,
    kind: "entities",
  }));
  const links = [];
  for (let i = 5; i < nodes.length; i++) {
    for (let hub = 0; hub < 5; hub++)
      links.push({
        source: nodes[i].id,
        target: nodes[hub].id,
        type: "related_to",
      });
  }
  const { page, context } = await pageFor();
  await page.route("**/api/graph", (route) =>
    route.fulfill({ json: { nodes, links } }),
  );
  await page.reload();
  await page.locator(".graph-canvas canvas").waitFor();
  await page.waitForTimeout(3500);
  const png = (
    await page.locator(".graph-canvas canvas").screenshot()
  ).toString("base64");
  const disks = await page.evaluate(async (png) => {
    const image = new Image();
    image.src = "data:image/png;base64," + png;
    await image.decode();
    const canvas = document.createElement("canvas");
    canvas.width = image.width;
    canvas.height = image.height;
    const ctx = canvas.getContext("2d");
    ctx.drawImage(image, 0, 0);
    const { data } = ctx.getImageData(0, 0, image.width, image.height);
    const green = new Uint8Array(image.width * image.height);
    // The DOM legend overlays the canvas; exclude its bottom strip.
    for (let i = 0; i < image.width * (image.height - 32); i++)
      green[i] = data[i * 4 + 1] - Math.max(data[i * 4], data[i * 4 + 2]) > 50;
    let disks = 0;
    for (let start = 0; start < green.length; start++) {
      if (!green[start]) continue;
      let pixels = 0;
      const pending = [start];
      green[start] = 0;
      while (pending.length) {
        const index = pending.pop();
        pixels++;
        const x = index % image.width,
          y = Math.floor(index / image.width);
        for (let dy = -1; dy <= 1; dy++)
          for (let dx = -1; dx <= 1; dx++) {
            if (
              x + dx < 0 ||
              x + dx >= image.width ||
              y + dy < 0 ||
              y + dy >= image.height
            )
              continue;
            const next = index + dy * image.width + dx;
            if (green[next]) {
              green[next] = 0;
              pending.push(next);
            }
          }
      }
      if (pixels > 3) disks++;
    }
    return disks;
  }, png);
  await page.screenshot({
    path: "/tmp/songya-ui-review/graph-dense-2d.png",
    fullPage: true,
  });
  assert.equal(
    disks,
    nodes.length,
    "Some rendered nodes overlap or are clipped",
  );
  await context.close();
  assert.deepEqual(errors, []);
});

test("WebGL failure leaves the library and inspector working", async () => {
  await save(source("No WebGL", "The library remains usable."));
  const { page, context } = await pageFor(undefined, () => {
    const original = HTMLCanvasElement.prototype.getContext;
    HTMLCanvasElement.prototype.getContext = function (type, ...args) {
      if (type.startsWith("webgl")) return null;
      return original.call(this, type, ...args);
    };
  });
  // The default view works without WebGL; only an explicit 3D choice needs it.
  await page.locator(".graph-canvas canvas").waitFor();
  assert.equal(
    await page
      .locator(".graph-canvas canvas")
      .evaluate((canvas) => !!canvas.getContext("2d")),
    true,
  );
  await page
    .getByRole("group", { name: "Graph view", exact: true })
    .getByRole("button", { name: "3D", exact: true })
    .click();
  await page
    .getByText("Graph unavailable. You can still browse the library.", {
      exact: true,
    })
    .waitFor();
  await editRecord(page, "No WebGL");
  assert.match(
    await page.locator(".ProseMirror").innerText(),
    /library remains usable/,
  );
  await page.keyboard.press("Escape");
  await page
    .getByRole("group", { name: "Graph view", exact: true })
    .getByRole("button", { name: "2D", exact: true })
    .click();
  await page.waitForFunction(
    () => !window.Alpine.$data(document.body).graphStatus,
  );
  assert.equal(
    await page
      .locator(".graph-canvas canvas")
      .evaluate((canvas) => !!canvas.getContext("2d")),
    true,
  );
  const search = page.getByRole("searchbox", {
    name: "Search graph",
    exact: true,
  });
  await search.fill("No WebGL");
  await page
    .locator("#graph-matches")
    .getByRole("button", { name: "No WebGL memories", exact: true })
    .waitFor();
  await context.close();
  assert.deepEqual(errors, []);
});

test("date controls preserve exact dates, open bounds and unknown values across editor modes", async () => {
  const record = await save(
    source(
      "Event time controls",
      "A dated event.",
      'time: {date: "2024-12-31", earliest: null, latest: null}\n',
    ),
  );
  const { page, context } = await pageFor();
  await editRecord(page, "Event time controls");
  await page.getByRole("button", { name: "Metadata", exact: true }).click();
  assert.equal(
    await page.getByLabel("Exact event date", { exact: true }).inputValue(),
    "2024-12-31",
  );
  await page
    .getByLabel("Earliest event date", { exact: true })
    .fill("2024-01-01");
  await page.getByLabel("Earliest event date", { exact: true }).blur();
  assert.equal(
    await page.getByLabel("Exact event date", { exact: true }).inputValue(),
    "",
  );
  await page.getByRole("tab", { name: "Source", exact: true }).click();
  assert.match(await page.locator(".cm-content").innerText(), /2024-01-01/);
  await page.getByRole("tab", { name: "Visual", exact: true }).click();
  await page.getByRole("button", { name: "Save", exact: true }).click();
  await waitText(page, "Saved");
  let doc = await (await fetch(url + "/api/records/" + record.id)).json();
  assert.deepEqual(doc.draft.fields.find((f) => f.key === "time").time, {
    date: null,
    earliest: "2024-01-01",
    latest: null,
  });
  await page.getByLabel("Earliest event date", { exact: true }).fill("");
  await page.getByLabel("Earliest event date", { exact: true }).blur();
  await page.getByRole("button", { name: "Save", exact: true }).click();
  await waitText(page, "Saved");
  doc = await (await fetch(url + "/api/records/" + record.id)).json();
  assert.deepEqual(doc.draft.fields.find((f) => f.key === "time").time, {
    date: null,
    earliest: null,
    latest: null,
  });
  await context.close();
  assert.deepEqual(errors, []);
});

test("story relation controls persist reading order while preserving prose", async () => {
  const first = await save(source("Reading first"));
  const second = await save(source("Reading second"));
  const story = await save(
    source(
      "Reading order",
      "Introduction",
      `relations: [{type: related_to, target: ${first.id}}, {type: related_to, target: ${second.id}}]\n`,
    ),
    "stories",
  );
  const { page, context } = await pageFor();
  await editRecord(page, "Reading order");
  await page.getByRole("button", { name: "Connections", exact: true }).click();
  await page
    .locator(".edge")
    .filter({ hasText: "Reading second" })
    .getByRole("button", { name: "Move connection up", exact: true })
    .click();
  await page.getByRole("button", { name: "Save", exact: true }).click();
  await waitText(page, "Saved");
  const doc = await (await fetch(url + "/api/records/" + story.id)).json();
  const ordered = doc.source.match(/target: (m_[a-z0-9]{12})/g);
  assert.deepEqual(ordered, ["target: " + second.id, "target: " + first.id]);
  assert.equal(doc.draft.body, "Introduction");
  await context.close();
  assert.deepEqual(errors, []);
});
