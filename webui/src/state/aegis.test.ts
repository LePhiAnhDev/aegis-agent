import { describe, expect, it } from "vitest";
import { defaultRestoreTarget } from "./aegis";

const snapshot = "4f2a9c1be7d0a3f5c6e8b9d0a1f2e3c4";

describe("defaultRestoreTarget", () => {
  it("restores next to the original outside Docker", () => {
    expect(defaultRestoreTarget("/srv/app/data", snapshot, "systemd")).toBe(
      "/srv/app/data-aegis-restore-4f2a9c1b",
    );
    expect(defaultRestoreTarget("/srv/app/data", snapshot, undefined)).toBe(
      "/srv/app/data-aegis-restore-4f2a9c1b",
    );
  });

  it("restores under /restores in Docker, where /userdata is read-only", () => {
    expect(
      defaultRestoreTarget("/userdata/srv/app/data", snapshot, "docker"),
    ).toBe("/restores/data-aegis-restore-4f2a9c1b");
    expect(
      defaultRestoreTarget("/userdata/srv/report.pdf", snapshot, "docker"),
    ).toBe("/restores/report.pdf-aegis-restore-4f2a9c1b");
  });

  it("offers the downloads folder for the whole snapshot", () => {
    expect(defaultRestoreTarget("/", snapshot, "docker")).toBe("");
    expect(defaultRestoreTarget("/", snapshot, "systemd")).toBe("");
  });
});
