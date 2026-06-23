import React, { useState, useEffect, useCallback } from "react";
import Modal from "./Modal";
import { Button } from "@/components/ui/button";
import { Textarea } from "@/components/ui/textarea";
import { Checkbox } from "@/components/ui/checkbox";
import { Badge } from "@/components/ui/badge";
import { featureFlagsApi, type FeatureFlag } from "../services/api";
import { refreshFeatureFlags } from "../services/featureFlagsStore";

interface FeatureFlagsModalProps {
  isOpen: boolean;
  onClose: () => void;
}

// Editor for a single flag. Keeps a draft string while editing; commits on
// blur / Save. Booleans get a checkbox; everything else gets a JSON textarea.
function FlagRow({
  flag,
  onSave,
  onClear,
}: {
  flag: FeatureFlag;
  onSave: (name: string, value: unknown) => Promise<void>;
  onClear: (name: string) => Promise<void>;
}) {
  const effective = flag.override !== undefined ? flag.override : flag.default;
  const overridden = flag.override !== undefined;
  const isBool = typeof effective === "boolean" || typeof flag.default === "boolean";

  const [draft, setDraft] = useState<string>(JSON.stringify(effective, null, 2));
  const [error, setError] = useState<string | null>(null);
  const [busy, setBusy] = useState(false);

  // Reset draft when the underlying flag changes (after save/refresh).
  useEffect(() => {
    setDraft(JSON.stringify(effective, null, 2));
    setError(null);
  }, [JSON.stringify(effective)]);

  const commitJSON = async () => {
    setError(null);
    let parsed: unknown;
    try {
      parsed = JSON.parse(draft);
    } catch (e) {
      setError(e instanceof Error ? e.message : "Invalid JSON");
      return;
    }
    try {
      setBusy(true);
      await onSave(flag.name, parsed);
    } catch (e) {
      setError(e instanceof Error ? e.message : "Save failed");
    } finally {
      setBusy(false);
    }
  };

  const toggleBool = async (next: boolean) => {
    setError(null);
    setBusy(true);
    try {
      await onSave(flag.name, next);
    } catch (e) {
      setError(e instanceof Error ? e.message : "Save failed");
    } finally {
      setBusy(false);
    }
  };

  const clear = async () => {
    setError(null);
    setBusy(true);
    try {
      await onClear(flag.name);
    } catch (e) {
      setError(e instanceof Error ? e.message : "Clear failed");
    } finally {
      setBusy(false);
    }
  };

  return (
    <div className="rounded-lg border border-border bg-card p-3 text-card-foreground">
      <div className="flex items-center gap-2">
        <code className="font-mono text-sm font-medium">{flag.name}</code>
        {overridden && (
          <Badge variant="secondary" className="rounded-md">
            overridden
          </Badge>
        )}
      </div>
      {flag.description && (
        <div className="mt-1 text-sm text-muted-foreground">{flag.description}</div>
      )}

      {isBool ? (
        <label className="mt-3 flex cursor-pointer items-center gap-2 text-sm">
          <Checkbox
            checked={effective === true}
            disabled={busy}
            onCheckedChange={(checked) => toggleBool(checked === true)}
          />
          <span className="font-mono">{effective === true ? "true" : "false"}</span>
        </label>
      ) : (
        <>
          <Textarea
            className="mt-3 font-mono text-xs"
            value={draft}
            onChange={(e) => setDraft(e.target.value)}
            spellCheck={false}
            rows={Math.min(8, draft.split("\n").length)}
            disabled={busy}
          />
          <div className="mt-2 flex justify-end">
            <Button
              size="sm"
              onClick={commitJSON}
              disabled={busy || draft === JSON.stringify(effective, null, 2)}
            >
              Save
            </Button>
          </div>
        </>
      )}

      <div className="mt-3 flex items-center justify-between gap-3 text-xs text-muted-foreground">
        <span>
          default: <code className="font-mono">{JSON.stringify(flag.default)}</code>
        </span>
        {overridden && (
          <Button variant="outline" size="sm" onClick={clear} disabled={busy}>
            Reset to default
          </Button>
        )}
      </div>

      {error && <div className="mt-2 text-sm text-destructive">{error}</div>}
    </div>
  );
}

function FeatureFlagsModal({ isOpen, onClose }: FeatureFlagsModalProps) {
  const [flags, setFlags] = useState<FeatureFlag[]>([]);
  const [loading, setLoading] = useState(false);
  const [error, setError] = useState<string | null>(null);

  const load = useCallback(async () => {
    setLoading(true);
    setError(null);
    try {
      setFlags(await featureFlagsApi.list());
    } catch (e) {
      setError(e instanceof Error ? e.message : "Failed to load feature flags");
    } finally {
      setLoading(false);
    }
  }, []);

  useEffect(() => {
    if (isOpen) load();
  }, [isOpen, load]);

  const handleSave = useCallback(
    async (name: string, value: unknown) => {
      await featureFlagsApi.set(name, value);
      await load();
      await refreshFeatureFlags();
    },
    [load],
  );

  const handleClear = useCallback(
    async (name: string) => {
      await featureFlagsApi.clear(name);
      await load();
      await refreshFeatureFlags();
    },
    [load],
  );

  return (
    <Modal isOpen={isOpen} onClose={onClose} title="Feature flags">
      {loading && <div className="text-sm text-muted-foreground">Loading…</div>}
      {error && <div className="text-sm text-destructive">{error}</div>}
      {!loading && !error && flags.length === 0 && (
        <div className="text-sm text-muted-foreground">
          No feature flags are defined. Add some by calling{" "}
          <code className="font-mono">featureflags.Register</code> in the Go code.
        </div>
      )}
      <div className="flex flex-col gap-3">
        {flags.map((f) => (
          <FlagRow key={f.name} flag={f} onSave={handleSave} onClear={handleClear} />
        ))}
      </div>
    </Modal>
  );
}

export default FeatureFlagsModal;
