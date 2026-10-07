import { readFileSync } from "node:fs";

import { http, HttpResponse } from "msw";
import { sample } from "openapi-sampler";
import { parse } from "yaml";

/**
 * MSW handlers generated from api/openapi/qavia.yaml (FE-S.6).
 *
 * Every response body comes from the contract's own schemas and examples, so a
 * component test's stub cannot drift from what the API returns: rename a field in
 * the spec and the stub renames with it. A test overrides only what it is about.
 */

type Operation = {
  operationId: string;
  responses: Record<string, { content?: Record<string, { schema?: unknown }> }>;
};
type Spec = { paths: Record<string, Record<string, Operation>> };

const spec = parse(
  readFileSync(new URL("../../../api/openapi/qavia.yaml", import.meta.url), "utf8"),
) as Spec;

const METHODS = ["get", "post", "put", "patch", "delete"] as const;

function successOf(operation: Operation): { status: number; schema?: unknown } {
  const code = Object.keys(operation.responses).find((c) => c.startsWith("2")) ?? "200";
  const schema = operation.responses[code]?.content?.["application/json"]?.schema;
  return { status: Number(code), schema };
}

function find(operationId: string) {
  for (const [path, item] of Object.entries(spec.paths)) {
    for (const method of METHODS) {
      const operation = item[method];
      if (operation?.operationId === operationId) return { path, method, operation };
    }
  }
  throw new Error(`operation ${operationId} is not in qavia.yaml`);
}

/** The example body the contract describes for an operation's success response. */
export function specExample<T = unknown>(operationId: string): T {
  const { schema } = successOf(find(operationId).operation);
  if (!schema) return undefined as T;
  return sample(schema as object, { skipReadOnly: false, skipWriteOnly: true }, spec) as T;
}

/** One handler per operation in the contract, answering with its example. */
export function specHandlers() {
  const handlers = [];
  for (const [path, item] of Object.entries(spec.paths)) {
    for (const method of METHODS) {
      const operation = item[method];
      if (!operation) continue;
      const { status, schema } = successOf(operation);
      const route = `*${path.replace(/\{([^}]+)\}/g, ":$1")}`;
      handlers.push(
        http[method](route, () =>
          schema
            ? HttpResponse.json(sample(schema as object, { skipWriteOnly: true }, spec) as object, {
                status,
              })
            : new HttpResponse(null, { status }),
        ),
      );
    }
  }
  return handlers;
}
