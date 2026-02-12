import React, { useState, useEffect, useCallback } from "react";
import { api } from "../services/api";
import { VersionInfo, CommitInfo } from "../types";

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

  if (!isOpen) return null;

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

  return (
    <div className="version-modal-overlay" onClick={onClose}>
      <div className="version-modal" onClick={(e) => e.stopPropagation()}>
        <div className="version-modal-header">
          <h2>Version</h2>
          <button onClick={onClose} className="version-modal-close" aria-label="Close">
            <svg fill="none" stroke="currentColor" viewBox="0 0 24 24">
              <path
                strokeLinecap="round"
                strokeLinejoin="round"
                strokeWidth={2}
                d="M6 18L18 6M6 6l12 12"
              />
            </svg>
          </button>
        </div>

        <div className="version-modal-content">
          {isLoading ? (
            <div className="version-loading">Checking for updates...</div>
          ) : versionInfo ? (
            <>
              <div className="version-info-row">
                <span className="version-label">Current:</span>
                <span className="version-value">
                  {versionInfo.current_tag || versionInfo.current_version || "dev"}
                </span>
                {versionInfo.current_commit_time && (
                  <span className="version-date">
                    ({formatDateTime(versionInfo.current_commit_time)})
                  </span>
                )}
              </div>

              {versionInfo.latest_tag && (
                <div className="version-info-row">
                  <span className="version-label">Latest:</span>
                  <span className="version-value">{versionInfo.latest_tag}</span>
                  {versionInfo.published_at && (
                    <span className="version-date">
                      ({formatDateTime(versionInfo.published_at)})
                    </span>
                  )}
                </div>
              )}

              {versionInfo.error && (
                <div className="version-error">
                  <span>Error: {versionInfo.error}</span>
                </div>
              )}

              {/* Changelog */}
              {versionInfo.has_update && (
                <div className="version-changelog">
                  <h3>
                    <a
                      href={`https://github.com/boldsoftware/shelley/compare/${versionInfo.current_tag}...${versionInfo.latest_tag}`}
                      target="_blank"
                      rel="noopener noreferrer"
                      className="changelog-link"
                    >
                      Changelog
                    </a>
                  </h3>
                  {loadingCommits ? (
                    <div className="version-loading">Loading...</div>
                  ) : commits.length > 0 ? (
                    <ul className="commit-list">
                      {commits.map((commit) => (
                        <li key={commit.sha} className="commit-item">
                          <a
                            href={getCommitUrl(commit.sha)}
                            target="_blank"
                            rel="noopener noreferrer"
                            className="commit-sha"
                          >
                            {commit.sha}
                          </a>
                          <span className="commit-message">{commit.message}</span>
                        </li>
                      ))}
                    </ul>
                  ) : (
                    <div className="version-no-commits">No commits found</div>
                  )}
                </div>
              )}

              {/* Auto-upgrade setting */}
              {!loadingAutoUpgrade && (
                <div className="version-auto-upgrade">
                  <label className="version-checkbox-label">
                    <input
                      type="checkbox"
                      checked={autoUpgrade}
                      onChange={(e) => handleAutoUpgradeChange(e.target.checked)}
                    />
                    <span>Auto-upgrade when idle (checks daily)</span>
                  </label>
                </div>
              )}

              {/* Upgrade & Restart button */}
              {versionInfo.has_update && versionInfo.download_url && (
                <div className="version-actions">
                  {upgradeError && <div className="version-error">{upgradeError}</div>}

                  <button
                    onClick={handleUpgradeAndRestart}
                    disabled={upgrading}
                    className="version-btn version-btn-primary"
                  >
                    {upgrading
                      ? versionInfo.running_under_systemd
                        ? "Upgrading & Restarting..."
                        : "Upgrading & Killing..."
                      : versionInfo.running_under_systemd
                        ? "Upgrade & Restart"
                        : "Upgrade & Kill Shelley Server"}
                  </button>
                </div>
              )}
            </>
          ) : (
            <div className="version-loading">Loading...</div>
          )}
        </div>
      </div>
    </div>
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
