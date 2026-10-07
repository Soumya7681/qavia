import { describe, expect, it } from "vitest";

import { extensionMatches, guessKind } from "./artifact-kinds";

describe("guessKind", () => {
  it("suggests a kind from the file name", () => {
    expect(guessKind("orders-openapi.yaml")).toBe("openapi");
    expect(guessKind("Orders.postman_collection.json")).toBe("postman");
    expect(guessKind("stories.md")).toBe("requirement_text");
    expect(guessKind("repo.tar.gz")).toBe("source_archive");
    expect(guessKind("schema.sql")).toBe("sql_dump");
    expect(guessKind("photo.png")).toBeUndefined();
  });

  it("checks an extension against a chosen kind", () => {
    expect(extensionMatches("openapi", "spec.YML")).toBe(true);
    expect(extensionMatches("sql_dump", "spec.yml")).toBe(false);
  });
});
