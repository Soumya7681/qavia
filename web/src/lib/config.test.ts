import { describe, expect, it } from "vitest";

import { parseApiBaseUrl } from "./config";

describe("parseApiBaseUrl", () => {
  it("defaults to the local API", () => {
    expect(parseApiBaseUrl(undefined)).toBe("http://localhost:8080");
    expect(parseApiBaseUrl("  ")).toBe("http://localhost:8080");
  });

  it("drops a trailing slash", () => {
    expect(parseApiBaseUrl("https://qavia.example.com/")).toBe("https://qavia.example.com");
    expect(parseApiBaseUrl("https://example.com/qavia//")).toBe("https://example.com/qavia");
  });

  it("rejects a relative or non-http value", () => {
    expect(() => parseApiBaseUrl("/api")).toThrow(/absolute URL/);
    expect(() => parseApiBaseUrl("ftp://example.com")).toThrow(/http or https/);
  });

  it("refuses credentials, because the value ships to the browser", () => {
    expect(() => parseApiBaseUrl("https://user:pass@example.com")).toThrow(/credentials/);
  });
});
