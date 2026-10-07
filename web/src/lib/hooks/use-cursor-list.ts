"use client";

import { useInfiniteQuery } from "@tanstack/react-query";

import { apiClient } from "@/lib/api/client";
import type { paths } from "@/lib/api/schema";

type GetPath = { [P in keyof paths]: paths[P] extends { get: object } ? P : never }[keyof paths];
type GetOp<P extends GetPath> = paths[P]["get"];
type OkBody<P extends GetPath> =
  GetOp<P> extends {
    responses: { 200: { content: { "application/json": infer B } } };
  }
    ? B
    : never;

/** GET endpoints that return a cursor page: `{ items, nextCursor }`. */
export type CursorPath = {
  [P in GetPath]: OkBody<P> extends { items: unknown[] } ? P : never;
}[GetPath];

export type CursorItem<P extends CursorPath> = OkBody<P> extends { items: (infer I)[] } ? I : never;

type Params<P extends CursorPath> = GetOp<P> extends { parameters: infer X } ? X : never;
type PathParams<P extends CursorPath> = Params<P> extends { path: infer X } ? X : never;
type QueryParams<P extends CursorPath> =
  Params<P> extends { query?: infer X } ? Omit<NonNullable<X>, "cursor"> : never;

export type CursorListInit<P extends CursorPath> = ([PathParams<P>] extends [never]
  ? { path?: never }
  : { path: PathParams<P> }) & { query?: QueryParams<P> };

/**
 * Every list screen's pagination (work-frontend.md rule 10): `?limit=&cursor=`
 * in, `nextCursor` out, never an offset and never an unbounded fetch.
 *
 * The query key is `["get", path, params]`, the same shape the generated hooks
 * use, so `invalidateQueries({ queryKey: ["get", path] })` refreshes it too.
 */
export function useCursorList<P extends CursorPath>(
  path: P,
  init: CursorListInit<P>,
  options: { enabled?: boolean } = {},
) {
  const query = useInfiniteQuery({
    queryKey: ["get", path, init],
    initialPageParam: undefined as string | undefined,
    queryFn: async ({ pageParam, signal }) => {
      const params = {
        path: init.path,
        query: { ...(init.query ?? {}), ...(pageParam ? { cursor: pageParam } : {}) },
      };
      // The path is one of the typed cursor endpoints; the generic call cannot be
      // narrowed further by the compiler, so it is widened here and only here.
      const get = apiClient.GET as unknown as (
        p: string,
        i: { params: unknown; signal?: AbortSignal },
      ) => Promise<{ data?: OkBody<P> }>;
      const { data } = await get(path, { params, signal });
      return data as OkBody<P>;
    },
    getNextPageParam: (last) =>
      (last as { nextCursor?: string | null } | undefined)?.nextCursor ?? undefined,
    enabled: options.enabled,
  });

  const items = (query.data?.pages ?? []).flatMap(
    (page) => (page as { items: CursorItem<P>[] }).items,
  );

  return {
    items,
    isLoading: query.isLoading,
    error: query.error,
    hasMore: query.hasNextPage,
    loadMore: () => query.fetchNextPage(),
    isLoadingMore: query.isFetchingNextPage,
    refetch: query.refetch,
  };
}
