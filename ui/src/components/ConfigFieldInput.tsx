import React from "react";
import { Label } from "@/components/ui/label";
import { Input } from "@/components/ui/input";
import {
  Select,
  SelectContent,
  SelectItem,
  SelectTrigger,
  SelectValue,
} from "@/components/ui/select";

interface ConfigField {
  name: string;
  label: string;
  type: string;
  required: boolean;
  placeholder?: string;
  description?: string;
  options?: string[];
}

interface ConfigFieldInputProps {
  field: ConfigField;
  value: string;
  onChange: (value: string) => void;
}

export default function ConfigFieldInput({ field, value, onChange }: ConfigFieldInputProps) {
  const inputId = `config-${field.name}`;
  const descId = `${inputId}-desc`;

  return (
    <div className="mb-3 flex flex-col gap-1.5">
      <Label htmlFor={inputId}>
        {field.label}
        {field.required && " *"}
      </Label>
      {field.options && field.options.length > 0 ? (
        <Select value={value} onValueChange={onChange}>
          <SelectTrigger
            id={inputId}
            className="w-full"
            aria-describedby={field.description ? descId : undefined}
          >
            <SelectValue placeholder="Select..." />
          </SelectTrigger>
          <SelectContent>
            {field.options.map((opt) => (
              <SelectItem key={opt} value={opt}>
                {opt}
              </SelectItem>
            ))}
          </SelectContent>
        </Select>
      ) : (
        <Input
          id={inputId}
          type={field.type === "password" ? "password" : "text"}
          value={value}
          onChange={(e) => onChange(e.target.value)}
          placeholder={field.placeholder}
          aria-describedby={field.description ? descId : undefined}
        />
      )}
      {field.description && (
        <span id={descId} className="text-xs text-muted-foreground">
          {field.description}
        </span>
      )}
    </div>
  );
}
