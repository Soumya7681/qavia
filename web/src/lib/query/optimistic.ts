import type { QueryClient, QueryKey } from "@tanstack/react-query";

/**
 * Mutation handlers for an inline edit that shows its result immediately and
 * puts the old value back if the API refuses (FE-S.5).
 *
 *   api.useMutation("patch", "/api/v1/test-cases/{id}", {
 *     ...optimistic(queryClient, key, (old, vars) => ({ ...old, ...vars.body })),
 *   })
 *
 * Settled either way, the query is refetched, so what stays on screen is what the
 * server stored, not what the client guessed.
 */
export function optimistic<TData, TVariables>(
  queryClient: QueryClient,
  queryKey: QueryKey,
  update: (current: TData, variables: TVariables) => TData,
) {
  return {
    onMutate: async (variables: TVariables) => {
      await queryClient.cancelQueries({ queryKey });
      const previous = queryClient.getQueryData<TData>(queryKey);
      if (previous !== undefined) {
        queryClient.setQueryData<TData>(queryKey, update(previous, variables));
      }
      return { previous };
    },
    onError: (
      _error: unknown,
      _variables: TVariables,
      context: { previous?: TData } | undefined,
    ) => {
      if (context && context.previous !== undefined) {
        queryClient.setQueryData<TData>(queryKey, context.previous);
      }
    },
    onSettled: () => queryClient.invalidateQueries({ queryKey }),
  };
}
