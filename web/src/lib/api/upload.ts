import { CORRELATION_HEADER } from "./client";
import { ApiError, ClientErrorCode } from "./errors";
import type { components } from "./schema";

type Artifact = components["schemas"]["Artifact"];
type ArtifactKind = components["schemas"]["ArtifactKind"];

/**
 * Uploads one input file with real progress (FE-0.6).
 *
 * XMLHttpRequest rather than fetch, because only it reports upload progress. The
 * path and the response type are the contract's (`POST /projects/{id}/artifacts`
 * returning `Artifact`); failures become the same ApiError every other call throws.
 */
export function uploadArtifact(
  projectID: string,
  kind: ArtifactKind,
  file: File,
  { onProgress, signal }: { onProgress?: (fraction: number) => void; signal?: AbortSignal } = {},
): Promise<Artifact> {
  return new Promise((resolve, reject) => {
    const xhr = new XMLHttpRequest();
    const correlationId = crypto.randomUUID();
    xhr.open("POST", `/api/v1/projects/${encodeURIComponent(projectID)}/artifacts`);
    xhr.setRequestHeader(CORRELATION_HEADER, correlationId);
    xhr.withCredentials = true;
    xhr.responseType = "text";

    xhr.upload.onprogress = (event) => {
      if (event.lengthComputable) onProgress?.(event.loaded / event.total);
    };
    xhr.onload = () => {
      let body: unknown;
      try {
        body = xhr.responseText ? JSON.parse(xhr.responseText) : undefined;
      } catch {
        body = undefined;
      }
      if (xhr.status >= 200 && xhr.status < 300) {
        onProgress?.(1);
        resolve(body as Artifact);
        return;
      }
      const envelope = body as {
        code?: string;
        message?: string;
        details?: Record<string, unknown>;
        incidentId?: string;
      };
      reject(
        new ApiError({
          code: envelope?.code ?? ClientErrorCode.UnexpectedResponse,
          message: envelope?.message ?? `The server answered ${xhr.status}.`,
          status: xhr.status,
          details: envelope?.details,
          incidentId: envelope?.incidentId,
          correlationId: xhr.getResponseHeader(CORRELATION_HEADER) ?? correlationId,
        }),
      );
    };
    xhr.onerror = () =>
      reject(
        new ApiError({
          code: ClientErrorCode.Network,
          message: "The upload could not reach Qavia. Check your connection and try again.",
          status: 0,
          correlationId,
        }),
      );
    xhr.onabort = () => reject(new DOMException("Upload cancelled", "AbortError"));
    signal?.addEventListener("abort", () => xhr.abort(), { once: true });

    const form = new FormData();
    form.append("kind", kind);
    form.append("file", file, file.name);
    xhr.send(form);
  });
}
