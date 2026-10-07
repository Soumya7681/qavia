import { QueryClient } from "@tanstack/react-query";
import { describe, expect, it } from "vitest";

import { optimistic } from "./optimistic";

type Case = { id: string; title: string };

describe("optimistic", () => {
  const key = ["get", "/api/v1/test-cases/{id}", { id: "1" }];

  it("shows the edit at once and rolls it back when the API refuses", async () => {
    const client = new QueryClient();
    client.setQueryData<Case>(key, { id: "1", title: "Old" });
    const handlers = optimistic<Case, { title: string }>(client, key, (old, v) => ({
      ...old,
      title: v.title,
    }));

    const context = await handlers.onMutate({ title: "New" });
    expect(client.getQueryData<Case>(key)?.title).toBe("New");

    handlers.onError(new Error("refused"), { title: "New" }, context);
    expect(client.getQueryData<Case>(key)?.title).toBe("Old");
  });

  it("invalidates when settled, so the server's value wins", async () => {
    const client = new QueryClient();
    client.setQueryData<Case>(key, { id: "1", title: "Old" });
    const handlers = optimistic<Case, { title: string }>(client, key, (old) => old);
    await handlers.onSettled();
    expect(client.getQueryState(key)?.isInvalidated).toBe(true);
  });
});
