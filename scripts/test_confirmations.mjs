import assert from "node:assert/strict";
import { readFile } from "node:fs/promises";
import { createRequire } from "node:module";

// Requires the frontend test server and the current containers build; all module API requests are intercepted here.
const require = createRequire(
  process.env.FRONTEND_PACKAGE ??
    new URL("../../../frontend/package.json", import.meta.url),
);
const { chromium, expect } = require("@playwright/test");
const base = process.env.FRONTEND_TEST_URL ?? "http://127.0.0.1:15177";
const bundle = await readFile(
  new URL("../dist/ui/index.js", import.meta.url),
  "utf8",
);
const stylesheet = await readFile(
  new URL("../dist/ui/index.css", import.meta.url),
  "utf8",
);
const labels = Object.fromEntries(
  await Promise.all(
    ["en", "ru", "uk"].map(async (lang) => [
      lang,
      JSON.parse(
        await readFile(
          new URL(`../frontend/locales/${lang}.json`, import.meta.url),
        ),
      ),
    ]),
  ),
);
const browser = await chromium.launch({
  executablePath: process.env.CHROME_PATH || undefined,
  headless: true,
  args: ["--no-sandbox"],
});
const escape = (value) => value.replace(/[.*+?^${}()|[\]\\]/g, "\\$&");
const actionName = (action, name) =>
  new RegExp(`^${escape(action)}.*${escape(name)}$`);

try {
  for (const lang of ["en", "ru", "uk"]) {
    for (const theme of ["light", "dark"]) {
      for (const material of ["solid", "glass"]) {
        for (const width of [320, 1440]) {
          const page = await browser.newPage({
            viewport: { width, height: 900 },
          });
          const requests = [];
          const errors = [];
          const seenRequests = [];
          const representative =
            lang === "en" &&
            theme === "light" &&
            material === "solid" &&
            width === 1440;
          page.on("pageerror", (error) => errors.push(error.message));
          page.on("request", (request) => seenRequests.push(request.url()));
          await page.route(
            "**/api/v1/module-assets/containers/index.js",
            (route) =>
              route.fulfill({
                contentType: "text/javascript; charset=utf-8",
                body: bundle,
              }),
          );
          await page.route(
            "**/api/v1/module-assets/containers/index.css",
            (route) =>
              route.fulfill({
                contentType: "text/css; charset=utf-8",
                body: stylesheet,
              }),
          );
          await page.route(
            "**/api/v1/module-api/containers/**",
            async (route) => {
              const url = new URL(route.request().url());
              const endpoint = url.pathname.split("/").at(-1);
              if (endpoint === "state")
                return route.fulfill({
                  json: {
                    engine: {
                      engine: "Docker fixture",
                      compose: "Compose fixture",
                      compatible: true,
                      reachable: true,
                      problem: "",
                      missing: [],
                      canInstall: false,
                      canStart: true,
                      dataRoot: "/fixture/docker",
                    },
                    containers: [
                      {
                        Id: "container-running-fixture",
                        Names: [
                          "/Backup worker with a long descriptive target name",
                        ],
                        Image: "fixture-image",
                        State: "running",
                        Status: "Up",
                        Labels: {},
                        Ports: [],
                      },
                      {
                        Id: "container-stopped-fixture",
                        Names: ["/Stopped worker"],
                        Image: "fixture-image",
                        State: "exited",
                        Status: "Exited",
                        Labels: {},
                        Ports: [],
                      },
                    ],
                    projects: [],
                    images: [],
                    networks: [],
                    volumes: [{ Name: "fixture-volume", Driver: "local" }],
                    migration: null,
                  },
                });
              if (endpoint === "jobs") return route.fulfill({ json: [] });
              if (endpoint === "action") {
                requests.push(route.request().postDataJSON());
                return route.fulfill({ json: {} });
              }
              return route.fulfill({ json: {} });
            },
          );
          await page.goto(
            `${base}/tests/bespoke-confirmations.html?module=containers&lang=${lang}&theme=${theme}&material=${material}`,
          );
          const t = labels[lang];
          const dialog = page.getByRole("dialog");
          const longName = "Backup worker with a long descriptive target name";
          const checkAction = async (action) => {
            const box = await action.boundingBox();
            assert.ok(
              box && box.x >= 0 && box.x + box.width <= width,
              `${lang}/${theme}/${material}/${width} action bounds`,
            );
            assert.ok(
              await page.evaluate(
                () => document.documentElement.scrollWidth <= innerWidth,
              ),
              `${lang}/${theme}/${material}/${width} no horizontal overflow`,
            );
            await expect(async () => {
              const hit = await action.evaluate((button) => {
                const rect = button.getBoundingClientRect();
                const element = document.elementFromPoint(
                  rect.x + rect.width / 2,
                  rect.y + rect.height / 2,
                );
                return element === button || button.contains(element);
              });
              expect(hit).toBe(true);
            }).toPass({ timeout: 3000 });
          };

          const runningRow = page
            .getByRole("row")
            .filter({ hasText: longName });
          const stopTrigger = runningRow.locator("button").nth(1);
          await expect(stopTrigger).toHaveAttribute(
            "aria-label",
            actionName(t.stop, longName),
          );
          await expect(stopTrigger)
            .toBeVisible({ timeout: 10000 })
            .catch(async (error) => {
              await page.screenshot({
                path: `/tmp/containers-confirmations-${lang}-${width}.png`,
                fullPage: true,
              });
              console.error(
                "Containers fixture debug:",
                lang,
                await page.locator("body").innerText(),
                seenRequests,
                errors,
              );
              throw error;
            });
          await stopTrigger.click();
          await expect(
            dialog.getByRole("heading", { name: actionName(t.stop, longName) }),
          ).toBeVisible();
          await expect(
            dialog.getByText(t.stopHint, { exact: true }),
          ).toBeVisible();
          const stop = dialog.getByRole("button", {
            name: actionName(t.stop, longName),
          });
          await expect(stop).toHaveClass(/primary/);
          await checkAction(stop);
          await expect(
            dialog.getByRole("button", { name: t.cancel, exact: true }),
          ).toBeFocused();
          await dialog
            .getByRole("button", { name: t.cancel, exact: true })
            .click();
          assert.deepEqual(
            requests,
            [],
            "cancelling Stop must not call the action endpoint",
          );

          const restartTrigger = runningRow.locator("button").nth(2);
          await expect(restartTrigger).toHaveAttribute(
            "aria-label",
            actionName(t.restart, longName),
          );
          await restartTrigger.click();
          await expect(
            dialog.getByRole("heading", {
              name: actionName(t.restart, longName),
            }),
          ).toBeVisible();
          await expect(
            dialog.getByText(t.restartHint, { exact: true }),
          ).toBeVisible();
          const restart = dialog.getByRole("button", {
            name: actionName(t.restart, longName),
          });
          await expect(restart).toHaveClass(/primary/);
          await checkAction(restart);
          await expect(
            dialog.getByRole("button", { name: t.cancel, exact: true }),
          ).toBeFocused();
          await dialog
            .getByRole("button", { name: t.cancel, exact: true })
            .click();
          assert.deepEqual(
            requests,
            [],
            "cancelling Restart must not call the action endpoint",
          );

          if (width < 640) {
            await page
              .getByRole("button", { name: new RegExp(escape(t.title)) })
              .click();
            await page
              .getByRole("button", { name: t.volumes, exact: true })
              .click();
          } else
            await page
              .getByRole("tab", { name: t.volumes, exact: true })
              .click();
          const volumeRow = page
            .getByRole("row")
            .filter({ hasText: "fixture-volume" });
          await expect(volumeRow.locator("button").first()).toHaveAttribute(
            "aria-label",
            actionName(t.remove, "fixture-volume"),
          );
          await volumeRow.locator("button").first().click();
          await expect(
            dialog.getByRole("heading", {
              name: actionName(t.remove, "fixture-volume"),
            }),
          ).toBeVisible();
          await expect(
            dialog.getByText(t.volumeWarning, { exact: true }),
          ).toBeVisible();
          const volumeRemove = dialog.getByRole("button", {
            name: actionName(t.remove, "fixture-volume"),
          });
          await expect(volumeRemove).toHaveClass(/danger/);
          await checkAction(volumeRemove);
          await expect(
            dialog.getByRole("button", { name: t.cancel, exact: true }),
          ).toBeFocused();
          await dialog
            .getByRole("button", { name: t.cancel, exact: true })
            .click();
          assert.deepEqual(
            requests,
            [],
            "cancelling volume removal must not call the action endpoint",
          );

          if (width < 640) {
            await page
              .getByRole("button", { name: new RegExp(escape(t.title)) })
              .click();
            await page
              .getByRole("button", { name: t.containers, exact: true })
              .click();
          } else
            await page
              .getByRole("tab", { name: t.containers, exact: true })
              .click();
          const stoppedRow = page
            .getByRole("row")
            .filter({ hasText: "Stopped worker" });
          const removeTrigger = stoppedRow.locator("button").last();
          await expect(removeTrigger).toHaveAttribute(
            "aria-label",
            actionName(t.remove, "Stopped worker"),
          );
          await removeTrigger.click();
          await expect(
            dialog.getByRole("heading", {
              name: actionName(t.remove, "Stopped worker"),
            }),
          ).toBeVisible();
          await expect(
            dialog.getByText(t.containerWarning, { exact: true }),
          ).toBeVisible();
          const containerRemove = dialog.getByRole("button", {
            name: actionName(t.remove, "Stopped worker"),
          });
          await expect(containerRemove).toHaveClass(/danger/);
          await checkAction(containerRemove);
          await expect(
            dialog.getByRole("button", { name: t.cancel, exact: true }),
          ).toBeFocused();
          if (representative) {
            await containerRemove.click();
            await expect.poll(() => requests.length).toBe(1);
            assert.equal(requests[0].action, "container.remove");
            assert.equal(requests[0].target, "container-stopped-fixture");
            assert.equal(requests[0].displayName, "Stopped worker");
            assert.match(requests[0].id, /^[a-f0-9]{32}$/);
          } else {
            await dialog
              .getByRole("button", { name: t.cancel, exact: true })
              .click();
            assert.deepEqual(
              requests,
              [],
              "cancelling container removal must not call the action endpoint",
            );
          }
          assert.deepEqual(
            errors,
            [],
            `${lang}/${theme}/${material}/${width} browser errors`,
          );
          console.log(
            `${lang}/${theme}/${material}/${width}: container, volume and service confirmations passed`,
          );
          await page.close();
        }
      }
    }
  }
  console.log(
    "PASS built containers bundle: EN/RU/UK × light/dark × solid/glass × 320/1440, including volume and container removal behavior",
  );
} finally {
  await browser.close();
}
