import assert from "node:assert/strict";
import { chromium } from "playwright";

async function browserStorage(page, value) {
  return page.evaluate(async (value) => {
    if (value !== null) {
      localStorage.setItem("shared-key", value);
      sessionStorage.setItem("shared-key", value);
    }
    const database = await new Promise((resolve, reject) => {
      const request = indexedDB.open("hex-origin-test", 1);
      request.onupgradeneeded = () =>
        request.result.createObjectStore("values");
      request.onsuccess = () => resolve(request.result);
      request.onerror = () => reject(request.error);
    });
    try {
      if (value !== null) {
        await new Promise((resolve, reject) => {
          const transaction = database.transaction("values", "readwrite");
          transaction.objectStore("values").put(value, "shared-key");
          transaction.oncomplete = resolve;
          transaction.onerror = () => reject(transaction.error);
        });
      }
      const stored = await new Promise((resolve, reject) => {
        const request = database
          .transaction("values")
          .objectStore("values")
          .get("shared-key");
        request.onsuccess = () => resolve(request.result ?? null);
        request.onerror = () => reject(request.error);
      });
      return {
        local: localStorage.getItem("shared-key"),
        session: sessionStorage.getItem("shared-key"),
        indexed: stored,
      };
    } finally {
      database.close();
    }
  }, value);
}

export async function verifyBrowserIsolation(port) {
  const browser = await chromium.launch();
  try {
    const context = await browser.newContext();
    const first = await context.newPage();
    const second = await context.newPage();
    const firstResponse = await first.goto(`http://demo.localhost:${port}/`);
    const secondResponse = await second.goto(`http://other.localhost:${port}/`);
    assert.equal(firstResponse.status(), 200);
    assert.equal(secondResponse.status(), 200);

    await browserStorage(first, "demo");
    assert.deepEqual(await browserStorage(second, null), {
      local: null,
      session: null,
      indexed: null,
    });
    await browserStorage(second, "other");
    assert.deepEqual(await browserStorage(first, null), {
      local: "demo",
      session: "demo",
      indexed: "demo",
    });
    assert.deepEqual(await browserStorage(second, null), {
      local: "other",
      session: "other",
      indexed: "other",
    });

    const apiStatus = await first.evaluate(
      async () => (await fetch("/api/hex/capabilities")).status,
    );
    assert.equal(apiStatus, 200);
    console.log(
      "Browser isolation passed: localStorage, sessionStorage and IndexedDB are separate per site origin.",
    );
  } finally {
    await browser.close();
  }
}
