import React from "react";

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
    <div className="form-group">
      <label htmlFor={inputId}>
        {field.label}
        {field.required && " *"}
      </label>
      {field.options && field.options.length > 0 ? (
        <select
          id={inputId}
          className="form-input"
          value={value}
          onChange={(e) => onChange(e.target.value)}
          aria-describedby={field.description ? descId : undefined}
        >
          <option value="">Select...</option>
          {field.options.map((opt) => (
            <option key={opt} value={opt}>
              {opt}
            </option>
          ))}
        </select>
      ) : (
        <input
          id={inputId}
          className="form-input"
          type={field.type === "password" ? "password" : "text"}
          value={value}
          onChange={(e) => onChange(e.target.value)}
          placeholder={field.placeholder}
          aria-describedby={field.description ? descId : undefined}
        />
      )}
      {field.description && (
        <span
          id={descId}
          style={{ fontSize: "0.75rem", color: "var(--text-secondary)", marginTop: "0.25rem", display: "block" }}
        >
          {field.description}
        </span>
      )}
    </div>
  );
}
