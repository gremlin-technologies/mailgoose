import { cleanup, render, screen, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { afterEach, describe, expect, it } from "vitest";

import type { components } from "../api/schema";
import type { Fields } from "./autosave";
import { iconUrl, SocialCanvas, SocialProperties, socialStyle } from "./social";

type Social = components["schemas"]["SocialBlock"];

const block = (extra: Partial<Social> = {}): Social =>
  ({
    id: "b_social0001",
    type: "social",
    links: [
      { network: "instagram", url: "https://instagram.com/bakery" },
      { network: "website", url: "https://bakery.example", label: "Our site" },
    ],
    align: "center",
    ...extra,
  }) as Social;

afterEach(cleanup);

describe("social block", () => {
  it("reads an old block with the defaults", () => {
    expect(socialStyle(block())).toEqual({ display: "icons", color: "brand", size: 32, spacing: 12, layout: "horizontal", align: "center" });
  });

  it("draws the API's icons on the canvas, named for people who can't see them", () => {
    render(<SocialCanvas block={block({ color: "dark", size: 40 })} />);
    const icons = within(screen.getByRole("list", { name: "Social links" })).getAllByRole("img");
    expect(icons.map((i) => [i.getAttribute("src"), i.getAttribute("alt"), i.getAttribute("width")])).toEqual([
      ["/assets/social/v1/dark/instagram.png", "Instagram", "40"],
      ["/assets/social/v1/dark/website.png", "Our site", "40"],
    ]);
  });

  it("shows names with or instead of icons", () => {
    const { rerender } = render(<SocialCanvas block={block({ display: "icons_and_text" })} />);
    expect(screen.getByText("Instagram")).toBeTruthy();
    expect(screen.getAllByRole("presentation")).toHaveLength(2); // the icons beside names have empty alt text
    rerender(<SocialCanvas block={block({ display: "text", layout: "vertical" })} />);
    expect(screen.queryAllByRole("img")).toHaveLength(0);
    expect(screen.getByText("Our site")).toBeTruthy();
    expect(screen.getByRole("list", { name: "Social links" }).style.flexDirection).toBe("column");
  });

  it("edits links and style, with the defaults picked for an old block", async () => {
    const sets: Fields[] = [];
    const user = userEvent.setup();
    render(<SocialProperties block={block()} set={(f) => sets.push(f)} />);
    expect((screen.getByRole("radio", { name: "Icons" }) as HTMLInputElement).checked).toBe(true);
    expect((screen.getByRole("radio", { name: "Brand colors" }) as HTMLInputElement).checked).toBe(true);
    expect((screen.getByRole("radio", { name: "Small" }) as HTMLInputElement).checked).toBe(true);

    await user.click(screen.getByRole("radio", { name: "White" }));
    await user.click(screen.getByRole("radio", { name: "Large" }));
    await user.click(screen.getByRole("radio", { name: "One a line" }));
    await user.click(screen.getByRole("radio", { name: "Icons and names" }));
    await user.click(within(screen.getByRole("group", { name: "Link 2" })).getByRole("button", { name: "Move up" }));
    expect(sets).toEqual([
      { color: "light" },
      { size: 48 },
      { layout: "vertical" },
      { display: "icons_and_text" },
      { links: [block().links[1], block().links[0]] },
    ]);

    sets.length = 0;
    await user.type(within(screen.getByRole("group", { name: "Link 1" })).getByRole("textbox", { name: "Name shown (optional)" }), "G");
    expect(sets).toEqual([{ links: [{ ...block().links[0], label: "G" }, block().links[1]] }]);
    expect(iconUrl("brand", "x")).toBe("/assets/social/v1/brand/x.png");
  });
});
