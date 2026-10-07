import type { components } from "./schema";

type Envelope = components["schemas"]["Error"];

/** Codes the client itself produces, for failures that never reached the API. */
export const ClientErrorCode = {
  /** The request did not get a response: offline, DNS, refused, reset. */
  Network: "network_error",
  /** A response arrived that is not the API's error envelope (a proxy page, say). */
  UnexpectedResponse: "unexpected_response",
} as const;

/**
 * Every failed call, from any endpoint, as one type.
 *
 * Screens branch on `code`, never on `message` (work-frontend.md rule 4). The
 * message is the API's own, written for a reader, and safe to show.
 */
export class ApiError extends Error {
  readonly code: string;
  readonly status: number;
  readonly details: Record<string, unknown>;
  /** Present on a 5xx, for a support report. */
  readonly incidentId?: string;
  /** The ID this request carried, which the API logs beside every line it writes. */
  readonly correlationId?: string;

  constructor(init: {
    code: string;
    message: string;
    status: number;
    details?: Record<string, unknown>;
    incidentId?: string;
    correlationId?: string;
    cause?: unknown;
  }) {
    super(init.message, { cause: init.cause });
    this.name = "ApiError";
    this.code = init.code;
    this.status = init.status;
    this.details = init.details ?? {};
    this.incidentId = init.incidentId;
    this.correlationId = init.correlationId;
  }

  get isNetworkError(): boolean {
    return this.code === ClientErrorCode.Network;
  }
}

export function isApiError(error: unknown): error is ApiError {
  return error instanceof ApiError;
}

function isEnvelope(value: unknown): value is Envelope {
  return (
    typeof value === "object" &&
    value !== null &&
    typeof (value as Envelope).code === "string" &&
    typeof (value as Envelope).message === "string"
  );
}

/** Reads a non-2xx response into an ApiError. Never throws itself. */
export async function apiErrorFromResponse(
  response: Response,
  correlationId?: string,
): Promise<ApiError> {
  const correlation = response.headers.get("X-Correlation-ID") ?? correlationId;

  let body: unknown;
  try {
    body = await response.clone().json();
  } catch {
    body = undefined;
  }

  if (isEnvelope(body)) {
    return new ApiError({
      code: body.code,
      message: body.message,
      status: response.status,
      details: body.details,
      incidentId: body.incidentId,
      correlationId: correlation,
    });
  }

  // Not the envelope: something between the browser and the API answered. Say so
  // plainly rather than inventing an API code for it.
  return new ApiError({
    code: ClientErrorCode.UnexpectedResponse,
    message: `The server answered ${response.status} ${response.statusText || ""}`.trim() + ".",
    status: response.status,
    correlationId: correlation,
  });
}

/** Wraps a fetch rejection. An abort is passed through: it is not a failure. */
export function apiErrorFromFetchFailure(error: unknown, correlationId?: string): unknown {
  if (error instanceof DOMException && error.name === "AbortError") {
    return error;
  }
  return new ApiError({
    code: ClientErrorCode.Network,
    message: "Qavia could not be reached. Check your connection and try again.",
    status: 0,
    correlationId,
    cause: error,
  });
}
