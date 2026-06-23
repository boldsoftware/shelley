import React, { useState, useEffect, useCallback } from "react";
import { api } from "../services/api";
import { VersionInfo, CommitInfo } from "../types";
import Modal from "./Modal";
import { Button } from "@/components/ui/button";
import { Checkbox } from "@/components/ui/checkbox";
import { Label } from "@/components/ui/label";
import { Separator } from "@/components/ui/separator";
import { Alert, AlertDescription } from "@/components/ui/alert";

interface VersionCheckerProps {
  onUpdateAvailable?: (hasUpdate: boolean) => void;
}

interface VersionModalProps {
  isOpen: boolean;
  onClose: () => void;
  versionInfo: VersionInfo | null;
  isLoading: boolean;
}

function VersionModal({ isOpen, onClose, versionInfo, isLoading }: VersionModalProps) {
  const [commits, setCommits] = useState<CommitInfo[]>([]);
  const [loadingCommits, setLoadingCommits] = useState(false);
  const [upgrading, setUpgrading] = useState(false);
  const [upgradeError, setUpgradeError] = useState<string | null>(null);
  const [autoUpgrade, setAutoUpgrade] = useState(false);
  const [loadingAutoUpgrade, setLoadingAutoUpgrade] = useState(true);
  const [upgradingHeadless, setUpgradingHeadless] = useState(false);
  const [headlessError, setHeadlessError] = useState<string | null>(null);
  const [headlessSuccess, setHeadlessSuccess] = useState<string | null>(null);

  useEffect(() => {
    if (isOpen) {
      if (versionInfo?.has_update && versionInfo.current_tag && versionInfo.latest_tag) {
        loadCommits(versionInfo.current_tag, versionInfo.latest_tag);
      }
      loadAutoUpgradeSetting();
    }
  }, [isOpen, versionInfo]);

  const loadAutoUpgradeSetting = async () => {
    setLoadingAutoUpgrade(true);
    try {
      const settings = await api.getSettings();
      setAutoUpgrade(settings.auto_upgrade === "true");
    } catch (err) {
      console.error("Failed to load auto-upgrade setting:", err);
    } finally {
      setLoadingAutoUpgrade(false);
    }
  };

  const handleAutoUpgradeChange = async (enabled: boolean) => {
    try {
      await api.setSetting("auto_upgrade", enabled ? "true" : "false");
      setAutoUpgrade(enabled);
    } catch (err) {
      console.error("Failed to set auto-upgrade:", err);
      // Revert the checkbox
      setAutoUpgrade(!enabled);
    }
  };

  const loadCommits = async (currentTag: string, latestTag: string) => {
    setLoadingCommits(true);
    try {
      const result = await api.getChangelog(currentTag, latestTag);
      setCommits(result || []);
    } catch (err) {
      console.error("Failed to load changelog:", err);
      setCommits([]);
    } finally {
      setLoadingCommits(false);
    }
  };

  const handleUpgradeAndRestart = async () => {
    setUpgrading(true);
    setUpgradeError(null);
    try {
      await api.upgrade(true);
    } catch (err) {
      // Connection drop is expected when server restarts, treat as success
      console.log("Upgrade response failed (expected during restart):", err);
    }
    // Wait a bit for server to restart, then reload the page
    setTimeout(() => {
      window.location.reload();
    }, 2000);
  };

  const handleUpgradeHeadlessShell = async () => {
    setUpgradingHeadless(true);
    setHeadlessError(null);
    setHeadlessSuccess(null);
    try {
      const result = await api.upgradeHeadlessShell();
      setHeadlessSuccess(result.message);
    } catch (err) {
      setHeadlessError(err instanceof Error ? err.message : String(err));
    } finally {
      setUpgradingHeadless(false);
    }
  };

  const formatDateTime = (dateStr: string) => {
    const date = new Date(dateStr);
    return date.toLocaleString(undefined, {
      year: "numeric",
      month: "short",
      day: "numeric",
      hour: "2-digit",
      minute: "2-digit",
      timeZoneName: "short",
    });
  };

  const getCommitUrl = (sha: string) => {
    return `https://github.com/boldsoftware/shelley/commit/${sha}`;
  };

  const InfoRow = ({
    label,
    value,
    date,
  }: {
    label: string;
    value: React.ReactNode;
    date?: string;
  }) => (
    <div className="flex flex-wrap items-baseline gap-x-2 gap-y-0.5 text-sm">
      <span className="text-muted-foreground">{label}</span>
      <span className="font-mono font-medium text-foreground">{value}</span>
      {date && <span className="text-xs text-muted-foreground">({formatDateTime(date)})</span>}
    </div>
  );

  return (
    <Modal isOpen={isOpen} onClose={onClose} title="Version" className="sm:max-w-2xl">
      {isLoading ? (
        <div className="py-8 text-center text-sm text-muted-foreground">
          Checking for updates...
        </div>
      ) : versionInfo ? (
        <div className="flex flex-col gap-4">
          <div className="flex flex-col gap-1">
            <InfoRow
              label="Current:"
              value={versionInfo.current_tag || versionInfo.current_version || "dev"}
              date={versionInfo.current_commit_time}
            />
            {versionInfo.latest_tag && (
              <InfoRow
                label="Latest:"
                value={versionInfo.latest_tag}
                date={versionInfo.published_at}
              />
            )}
          </div>

          {versionInfo.error && (
            <Alert variant="destructive">
              <AlertDescription>Error: {versionInfo.error}</AlertDescription>
            </Alert>
          )}

          {/* Changelog */}
          {versionInfo.has_update && (
            <div className="flex flex-col gap-2">
              <h3 className="text-sm font-semibold">
                <a
                  href={`https://github.com/boldsoftware/shelley/compare/${versionInfo.current_tag}...${versionInfo.latest_tag}`}
                  target="_blank"
                  rel="noopener noreferrer"
                  className="text-primary underline-offset-4 hover:underline"
                >
                  Changelog
                </a>
              </h3>
              {loadingCommits ? (
                <div className="py-2 text-sm text-muted-foreground">Loading...</div>
              ) : commits.length > 0 ? (
                <ul className="flex max-h-72 flex-col gap-1.5 overflow-y-auto rounded-md border border-border bg-muted/40 p-3">
                  {commits.map((commit) => (
                    <li key={commit.sha} className="flex gap-2 text-sm">
                      <a
                        href={getCommitUrl(commit.sha)}
                        target="_blank"
                        rel="noopener noreferrer"
                        className="shrink-0 font-mono text-xs text-primary underline-offset-4 hover:underline"
                      >
                        {commit.sha}
                      </a>
                      <span className="min-w-0 break-words text-foreground">{commit.message}</span>
                    </li>
                  ))}
                </ul>
              ) : (
                <div className="py-2 text-sm text-muted-foreground">No commits found</div>
              )}
            </div>
          )}

          <Separator />

          {/* Footer: auto-upgrade + upgrade button */}
          <div className="flex flex-col gap-4">
            {!loadingAutoUpgrade && (
              <Label className="flex items-center gap-2 text-sm font-normal">
                <Checkbox
                  checked={autoUpgrade}
                  onCheckedChange={(checked) => handleAutoUpgradeChange(checked === true)}
                />
                <span>Auto-upgrade when idle (checks daily)</span>
              </Label>
            )}

            {versionInfo.has_update && versionInfo.download_url && (
              <div className="flex flex-col gap-2">
                {upgradeError && (
                  <Alert variant="destructive">
                    <AlertDescription>{upgradeError}</AlertDescription>
                  </Alert>
                )}

                <Button
                  onClick={handleUpgradeAndRestart}
                  disabled={upgrading}
                  className="self-start"
                >
                  {upgrading
                    ? versionInfo.running_under_systemd
                      ? "Upgrading Shelley & Restarting..."
                      : "Upgrading Shelley & Killing..."
                    : versionInfo.running_under_systemd
                      ? "Upgrade Shelley & Restart"
                      : "Upgrade & Kill Shelley Server"}
                </Button>
              </div>
            )}

            {/* Headless Shell (Browser) section */}
            {versionInfo.headless_shell_current && (
              <div className="flex flex-col gap-2 border-t border-border pt-4">
                <InfoRow label="Browser:" value={versionInfo.headless_shell_current} />
                {versionInfo.headless_shell_update && versionInfo.headless_shell_latest && (
                  <InfoRow label="Latest:" value={versionInfo.headless_shell_latest} />
                )}
                {versionInfo.headless_shell_update ? (
                  <div className="flex flex-col gap-2">
                    {headlessError && (
                      <Alert variant="destructive">
                        <AlertDescription>{headlessError}</AlertDescription>
                      </Alert>
                    )}
                    {headlessSuccess && (
                      <Alert>
                        <AlertDescription>{headlessSuccess}</AlertDescription>
                      </Alert>
                    )}
                    <Button
                      onClick={handleUpgradeHeadlessShell}
                      disabled={upgradingHeadless}
                      variant="secondary"
                      className="self-start"
                    >
                      {upgradingHeadless ? "Upgrading Browser..." : "Upgrade Browser"}
                    </Button>
                  </div>
                ) : (
                  <div className="text-sm text-muted-foreground">Browser is up to date</div>
                )}
              </div>
            )}
          </div>
        </div>
      ) : (
        <div className="py-8 text-center text-sm text-muted-foreground">Loading...</div>
      )}
    </Modal>
  );
}

export function useVersionChecker({ onUpdateAvailable }: VersionCheckerProps = {}) {
  const [versionInfo, setVersionInfo] = useState<VersionInfo | null>(null);
  const [showModal, setShowModal] = useState(false);
  const [isLoading, setIsLoading] = useState(false);
  const [shouldNotify, setShouldNotify] = useState(false);

  const checkVersion = useCallback(async () => {
    setIsLoading(true);
    try {
      // Always force refresh when checking
      const info = await api.checkVersion(true);
      setVersionInfo(info);
      setShouldNotify(info.should_notify);
      onUpdateAvailable?.(info.should_notify);
    } catch (err) {
      console.error("Failed to check version:", err);
    } finally {
      setIsLoading(false);
    }
  }, [onUpdateAvailable]);

  // Check version on mount (uses cache)
  useEffect(() => {
    const checkInitial = async () => {
      try {
        const info = await api.checkVersion(false);
        setVersionInfo(info);
        setShouldNotify(info.should_notify);
        onUpdateAvailable?.(info.should_notify);
      } catch (err) {
        console.error("Failed to check version:", err);
      }
    };
    checkInitial();
  }, [onUpdateAvailable]);

  const openModal = useCallback(() => {
    setShowModal(true);
    // Always check for new version when opening modal
    checkVersion();
  }, [checkVersion]);

  const closeModal = useCallback(() => {
    setShowModal(false);
  }, []);

  const VersionModalComponent = (
    <VersionModal
      isOpen={showModal}
      onClose={closeModal}
      versionInfo={versionInfo}
      isLoading={isLoading}
    />
  );

  return {
    hasUpdate: shouldNotify, // For red dot indicator (5+ days apart)
    versionInfo,
    openModal,
    closeModal,
    isLoading,
    VersionModal: VersionModalComponent,
  };
}

export default useVersionChecker;
