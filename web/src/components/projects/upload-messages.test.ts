import { describe, expect, it } from "vitest";

import { ApiError } from "@/lib/api/errors";

import { uploadRejection } from "./upload-messages";

const err = (code: string, details: Record<string, unknown> = {}, message = code) =>
  new ApiError({ code, status: 400, message, details });

describe("uploadRejection", () => {
  it("explains every rejection reason distinctly", () => {
    const titles = [
      "upload_too_large",
      "unsupported_media_type",
      "archive_rejected",
      "upload_corrupt",
    ].map((code) => uploadRejection(err(code)).title);
    expect(new Set(titles).size).toBe(4);
  });

  it("states the size and the limit", () => {
    const file = new File([new Uint8Array(60 * 1024 * 1024)], "big.yaml");
    expect(
      uploadRejection(err("upload_too_large", { limitBytes: 50 * 1024 * 1024 }), file).description,
    ).toBe("It is 60 MB; the limit is 50 MB. An admin can raise it in Settings, Uploads.");
  });

  it("says what the content really was", () => {
    const d = uploadRejection(
      err("unsupported_media_type", {
        detected: "image/png",
        allowed: ["application/json", "text/plain"],
      }),
    ).description;
    expect(d).toContain("image/png");
    expect(d).toContain("application/json, text/plain");
  });

  it("passes the server's archive and parse reasons through", () => {
    expect(
      uploadRejection(err("archive_rejected", {}, "Entry ../../etc escapes the archive."))
        .description,
    ).toBe("Entry ../../etc escapes the archive.");
  });
});
