import { expect, test } from "@playwright/test";

// Workspace mode's drawer leads with exe's logo; stock Shelley's
// drawer doesn't have it.
test("workspace mode shows the exe logo atop the drawer, stock Shelley doesn't", async ({
  page,
  request,
}) => {
  await page.setViewportSize({ width: 1280, height: 800 });
  const on = await request.post("/feature-flags", { data: { name: "workspace", value: true } });
  expect(on.ok()).toBeTruthy();
  try {
    await page.goto("/");
    const brand = page.locator(".drawer-header [data-testid='exe-brand']");
    await expect(brand).toBeVisible();
    await expect(brand).toHaveText("exe");
    const fish = brand.locator("img");
    await expect
      .poll(() => fish.evaluate((img: HTMLImageElement) => img.naturalWidth))
      .toBeGreaterThan(0);
    const box = await fish.boundingBox();
    expect(box?.width).toBeGreaterThan(16);
  } finally {
    const off = await request.delete("/feature-flags", { data: { name: "workspace" } });
    expect(off.ok()).toBeTruthy();
  }
  await page.goto("/");
  await expect(page.locator(".drawer-header")).toBeVisible();
  await expect(page.getByTestId("exe-brand")).toHaveCount(0);
});
