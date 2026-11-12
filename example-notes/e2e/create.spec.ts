import { expect, test } from "@playwright/test";

test("adds a note", async ({ page }) => {
  await page.goto("/");
  await page.getByLabel("Title").fill("Call the plumber");
  await page.getByLabel("Note").fill("before Friday");
  await page.getByRole("button", { name: "Add" }).click();
  await expect(page.getByRole("listitem").filter({ hasText: "Call the plumber" })).toBeVisible();
});

test("refuses a note without a title", async ({ page }) => {
  await page.goto("/");
  await page.getByLabel("Note").fill("no title");
  await page.getByRole("button", { name: "Add" }).click();
  await expect(page.getByRole("alert")).toHaveText(/needs a title/);
});
