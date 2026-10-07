"use client";

import { XIcon } from "lucide-react";
import { useState } from "react";
import { type Control, Controller, type UseFormRegister } from "react-hook-form";

import { Badge } from "@/components/ui/badge";
import { Button } from "@/components/ui/button";
import { Input } from "@/components/ui/input";
import {
  Select,
  SelectContent,
  SelectItem,
  SelectTrigger,
  SelectValue,
} from "@/components/ui/select";
import { Switch } from "@/components/ui/switch";
import { Textarea } from "@/components/ui/textarea";

import { fieldName, type SettingEntry } from "./registry";

type Props = {
  entry: SettingEntry;
  id: string;
  control: Control<Record<string, unknown>>;
  register: UseFormRegister<Record<string, unknown>>;
  invalid: boolean;
  disabled?: boolean;
  describedBy?: string;
};

function StringList({
  value,
  onChange,
  id,
  disabled,
  invalid,
}: {
  value: string[];
  onChange: (next: string[]) => void;
  id: string;
  disabled?: boolean;
  invalid: boolean;
}) {
  const [draft, setDraft] = useState("");
  const add = () => {
    const item = draft.trim();
    if (item && !value.includes(item)) onChange([...value, item]);
    setDraft("");
  };
  return (
    <div className="flex flex-col gap-2">
      {value.length > 0 ? (
        <ul className="flex flex-wrap gap-1.5" aria-label="Current values">
          {value.map((item) => (
            <li key={item}>
              <Badge variant="secondary" className="gap-1 pr-1 font-mono">
                {item}
                <button
                  type="button"
                  disabled={disabled}
                  onClick={() => onChange(value.filter((v) => v !== item))}
                  className="rounded-sm p-0.5 hover:bg-foreground/10"
                  aria-label={`Remove ${item}`}
                >
                  <XIcon className="size-3" aria-hidden />
                </button>
              </Badge>
            </li>
          ))}
        </ul>
      ) : null}
      <div className="flex gap-2">
        <Input
          id={id}
          value={draft}
          disabled={disabled}
          aria-invalid={invalid}
          placeholder="Add a value and press Enter"
          onChange={(e) => setDraft(e.target.value)}
          onKeyDown={(e) => {
            if (e.key === "Enter") {
              e.preventDefault();
              add();
            }
          }}
        />
        <Button type="button" variant="outline" onClick={add} disabled={disabled || !draft.trim()}>
          Add
        </Button>
      </div>
    </div>
  );
}

/**
 * One control per registry kind. A new kind is a new case here, never a new screen
 * (FE-0.4). Secrets are handled by the caller, which owns the Replace flow.
 */
export function SettingControl({
  entry,
  id,
  control,
  register,
  invalid,
  disabled,
  describedBy,
}: Props) {
  const name = fieldName(entry.key);
  const common = { id, disabled, "aria-invalid": invalid, "aria-describedby": describedBy };
  const schema = entry.schema as { enum?: string[]; minimum?: number; maximum?: number };

  switch (entry.kind) {
    case "bool":
      return (
        <Controller
          control={control}
          name={name}
          render={({ field }) => (
            <Switch
              {...common}
              checked={field.value === true}
              onCheckedChange={(checked) => field.onChange(checked)}
            />
          )}
        />
      );
    case "enum":
      return (
        <Controller
          control={control}
          name={name}
          render={({ field }) => (
            <Select
              value={String(field.value ?? "")}
              onValueChange={field.onChange}
              disabled={disabled}
            >
              <SelectTrigger {...common} className="w-full sm:w-72">
                <SelectValue />
              </SelectTrigger>
              <SelectContent>
                {(schema.enum ?? []).map((option) => (
                  <SelectItem key={option} value={option}>
                    {option}
                  </SelectItem>
                ))}
              </SelectContent>
            </Select>
          )}
        />
      );
    case "int":
    case "number":
      return (
        <Input
          {...common}
          type="number"
          inputMode={entry.kind === "int" ? "numeric" : "decimal"}
          step={entry.kind === "int" ? 1 : "any"}
          min={schema.minimum}
          max={schema.maximum}
          className="w-full sm:w-48"
          {...register(name, { valueAsNumber: true })}
        />
      );
    case "string_list":
      return (
        <Controller
          control={control}
          name={name}
          render={({ field }) => (
            <StringList
              id={id}
              value={(field.value as string[]) ?? []}
              onChange={field.onChange}
              disabled={disabled}
              invalid={invalid}
            />
          )}
        />
      );
    case "object":
      return <Textarea {...common} rows={6} className="font-mono text-xs" {...register(name)} />;
    case "duration":
      return (
        <Input
          {...common}
          className="w-full font-mono sm:w-48"
          placeholder="5m"
          spellCheck={false}
          {...register(name)}
        />
      );
    case "secret":
      return (
        <Input
          {...common}
          type="password"
          autoComplete="off"
          spellCheck={false}
          {...register(name)}
        />
      );
    default:
      return <Input {...common} spellCheck={false} {...register(name)} />;
  }
}
