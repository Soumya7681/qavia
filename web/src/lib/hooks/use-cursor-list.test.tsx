// @vitest-environment happy-dom
import { act, waitFor } from "@testing-library/react";
import { http, HttpResponse } from "msw";
import { describe, expect, it } from "vitest";

import { server, setupMockApi } from "@/test/msw";
import { renderHookWithProviders } from "@/test/render";

import { useCursorList } from "./use-cursor-list";

setupMockApi();

describe("useCursorList", () => {
  it("follows nextCursor page by page and stops at the end", async () => {
    const cursors: (string | null)[] = [];
    server.use(
      http.get("*/api/v1/projects", ({ request }) => {
        const url = new URL(request.url);
        cursors.push(url.searchParams.get("cursor"));
        expect(url.searchParams.get("limit")).toBe("2");
        return url.searchParams.get("cursor") === "page-2"
          ? HttpResponse.json({ items: [{ id: "c", name: "C" }] })
          : HttpResponse.json({
              items: [
                { id: "a", name: "A" },
                { id: "b", name: "B" },
              ],
              nextCursor: "page-2",
            });
      }),
    );

    const { result } = renderHookWithProviders(() =>
      useCursorList("/api/v1/projects", { query: { limit: 2 } }),
    );

    await waitFor(() => expect(result.current.items).toHaveLength(2));
    expect(result.current.hasMore).toBe(true);

    await act(() => result.current.loadMore());
    await waitFor(() => expect(result.current.items.map((p) => p.name)).toEqual(["A", "B", "C"]));
    expect(result.current.hasMore).toBe(false);
    expect(cursors).toEqual([null, "page-2"]);
  });
});
