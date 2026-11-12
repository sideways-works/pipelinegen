import { expect, test } from "@playwright/test";

test("finds a note by a word in it", async ({ page }) => {
  await page.goto("/");
  await page.getByLabel("Search").fill("Milk");
  await page.getByRole("button", { name: "Search" }).click();
  await expect(page.getByRole("listitem")).toHaveCount(1);
  await expect(page.getByRole("listitem")).toHaveText("Groceries: milk, eggs");
});
