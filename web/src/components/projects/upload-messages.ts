import type { ApiError } from "@/lib/api/errors";
import { formatBytes } from "@/lib/format";

/**
 * A rejected upload, explained by its reason (FE-0.6): each code gets its own
 * sentence and the details that make it actionable.
 */
export function uploadRejection(
  error: ApiError,
  file?: File,
): { title: string; description?: string } {
  switch (error.code) {
    case "upload_too_large": {
      const limit = Number(error.details.limitBytes);
      return {
        title: "This file is too large",
        description: Number.isFinite(limit)
          ? `${file ? `It is ${formatBytes(file.size)}; ` : ""}the limit is ${formatBytes(limit)}. An admin can raise it in Settings, Uploads.`
          : error.message,
      };
    }
    case "unsupported_media_type": {
      const detected =
        typeof error.details.detected === "string" ? error.details.detected : undefined;
      const allowed = Array.isArray(error.details.allowed)
        ? (error.details.allowed as string[])
        : [];
      return {
        title: "Qavia does not accept this type of file",
        description: [
          detected ? `Its content looks like ${detected}, whatever its name says.` : undefined,
          allowed.length ? `Accepted: ${allowed.join(", ")}.` : undefined,
        ]
          .filter(Boolean)
          .join(" "),
      };
    }
    case "archive_rejected":
      return { title: "This archive was refused", description: error.message };
    case "upload_corrupt":
      return { title: "This file could not be read", description: error.message };
    case "project_archived":
      return {
        title: "This project is archived",
        description: "Unarchive it to upload new inputs.",
      };
    default:
      return { title: "The upload failed", description: error.message };
  }
}
