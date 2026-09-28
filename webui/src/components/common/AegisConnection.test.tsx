import { describe, expect, it } from "vitest";
import { formatTime } from "../../lib/formatting";
import * as m from "../../paraglide/messages";
import type { AegisStatus } from "../../state/aegis";
import { aegisStatusDetails } from "./AegisConnection";

const status = (overrides: Partial<AegisStatus>): AegisStatus => ({
  enabled: true,
  state: "connected",
  pending: 0,
  repositories: [],
  ...overrides,
});

describe("aegisStatusDetails", () => {
  it("shows the last report at the time the agent sent it", () => {
    const details = aegisStatusDetails(
      status({ lastSuccessAt: "2026-09-28T07:23:42Z", pending: 3 }),
    );
    expect(details).toBe(
      m.aegis_status_details({
        lastReport: formatTime(Date.UTC(2026, 8, 28, 7, 23, 42)),
        pending: "3",
      }),
    );
    expect(details).not.toContain("1970");
  });

  it("says never before the first report and adds why reporting stopped", () => {
    const details = aegisStatusDetails(
      status({
        state: "stopped",
        lastError: "This report token was replaced.",
      }),
    );
    expect(details).toContain(m.aegis_status_never());
    expect(details.endsWith(" This report token was replaced.")).toBe(true);
  });
});
