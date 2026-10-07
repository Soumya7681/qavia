import { z } from "zod";

import type { components } from "@/lib/api/schema";

export type SettingEntry = components["schemas"]["SettingEntry"];
export type SettingValue = components["schemas"]["SettingValue"];
export type SettingScope = components["schemas"]["SettingScope"];

/**
 * Setting keys contain dots, which React Hook Form reads as nested paths. Field
 * names are the key with dots swapped for a character keys never contain.
 */
export const fieldName = (key: string) => key.replaceAll(".", "~");
export const keyOf = (name: string) => name.replaceAll("~", ".");

/**
 * The validator for one entry, built from the JSON Schema the registry serves
 * (tech-stack.md 11): one declaration in Go, enforced in the browser and again by
 * the API. A kind needs code here only when the form holds it differently from
 * how it is sent.
 */
export function validatorFor(entry: SettingEntry): z.ZodType {
  if (entry.kind === "secret") {
    return z.string().min(1, `Enter the new ${entry.label.toLowerCase()}, or cancel.`);
  }
  if (entry.kind === "object") {
    // Edited as JSON text; parsed here so the schema sees the object.
    return z
      .string()
      .transform((text, ctx) => {
        try {
          return JSON.parse(text) as unknown;
        } catch {
          ctx.addIssue({ code: "custom", message: "This is not valid JSON." });
          return z.NEVER;
        }
      })
      .pipe(z.fromJSONSchema(entry.schema as never));
  }
  if (entry.kind === "duration") {
    // The schema's pattern, with a message a person can act on.
    const pattern = (entry.schema as { pattern?: string }).pattern;
    return pattern
      ? z.string().regex(new RegExp(pattern), "Use a duration like 30s, 5m, or 2h.")
      : z.string().min(1);
  }
  return z.fromJSONSchema(entry.schema as never);
}

/** The value a control holds for an entry, from what the API reports. */
export function formValueOf(entry: SettingEntry, value: SettingValue | undefined): unknown {
  const raw = value?.value ?? entry.default;
  switch (entry.kind) {
    case "secret":
      return "";
    case "object":
      return JSON.stringify(raw ?? {}, null, 2);
    case "string_list":
      return Array.isArray(raw) ? raw : [];
    case "bool":
      return raw === true;
    default:
      return raw ?? (entry.kind === "int" || entry.kind === "number" ? 0 : "");
  }
}

/** The scopes a screen writes to, in order of preference. */
export function writableScope(entry: SettingEntry, screen: SettingScope): SettingScope | null {
  return entry.scopes.includes(screen) ? screen : null;
}

/** "Default", "Set here", or "Inherited from global". */
export function sourceLabel(value: SettingValue | undefined, screen: SettingScope): string {
  if (!value || value.fromDefault) return "Default";
  if (value.source === screen) return "Set here";
  return `Inherited from ${value.source}`;
}
