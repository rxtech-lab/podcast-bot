import { expect, test, type Locator } from "@playwright/test";

const firstModel = "gpt-4o-mini";
const secondModel = "gpt-4o";
const firstVoice = "en-US-E2EAvaNeural";
const secondVoice = "en-US-E2ENovaNeural";

async function addAllowlistValue(
  dialog: Locator,
  field: "models_allow" | "voices_allow",
  index: number,
  value: string,
) {
  await dialog.locator(`#root_${field}__add`).click();
  await dialog
    .locator(`#root_${field}_${index}`)
    .selectOption({ label: value });
}

async function expectAllowlistValue(
  dialog: Locator,
  field: "models_allow" | "voices_allow",
  index: number,
  value: string,
) {
  const select = dialog.locator(`#root_${field}_${index}`);
  await expect(select).toBeVisible();
  await expect
    .poll(() =>
      select.evaluate((element) => {
        const option = (element as HTMLSelectElement).selectedOptions.item(0);
        return option?.textContent?.trim() ?? "";
      }),
    )
    .toBe(value);
}

test("creates a permission role and keeps added models and voices on update", async ({
  page,
}) => {
  await page.goto("/admin/subscription-permissions");

  await page.getByRole("button", { name: "+ Create" }).click();
  let dialog = page.getByRole("dialog", {
    name: "Create Subscription Permissions",
  });

  await dialog
    .locator("#root_subscription_class")
    .selectOption({ label: "No subscription (free)" });
  await dialog.locator("#root_models_mode").selectOption("only");
  await addAllowlistValue(dialog, "models_allow", 0, firstModel);
  await dialog.locator("#root_voices_mode").selectOption("only");
  await addAllowlistValue(dialog, "voices_allow", 0, firstVoice);
  await dialog.getByRole("button", { name: "Create", exact: true }).click();

  await expect(dialog).toBeHidden();
  const permissionRow = page
    .getByRole("cell", { name: "Free (no subscription)" })
    .locator("..");
  await expect(permissionRow).toBeVisible();
  await expect(permissionRow).toContainText("models: only · voices: only");

  await permissionRow.getByRole("button", { name: "Edit" }).click();
  dialog = page.getByRole("dialog", {
    name: "Edit Subscription Permissions",
  });
  await expectAllowlistValue(dialog, "models_allow", 0, firstModel);
  await expectAllowlistValue(dialog, "voices_allow", 0, firstVoice);

  await addAllowlistValue(dialog, "models_allow", 1, secondModel);
  await addAllowlistValue(dialog, "voices_allow", 1, secondVoice);
  await dialog.getByRole("button", { name: "Save", exact: true }).click();
  await expect(dialog).toBeHidden();

  await permissionRow.getByRole("button", { name: "Edit" }).click();
  dialog = page.getByRole("dialog", {
    name: "Edit Subscription Permissions",
  });
  await expectAllowlistValue(dialog, "models_allow", 0, firstModel);
  await expectAllowlistValue(dialog, "models_allow", 1, secondModel);
  await expectAllowlistValue(dialog, "voices_allow", 0, firstVoice);
  await expectAllowlistValue(dialog, "voices_allow", 1, secondVoice);
});
