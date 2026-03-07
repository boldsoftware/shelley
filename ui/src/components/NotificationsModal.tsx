import React, { useState, useEffect, useCallback } from "react";
import Modal from "./Modal";
import { useI18n } from "../i18n";
import ConfigFieldInput from "./ConfigFieldInput";
import { notificationChannelsApi, NotificationChannelAPI, ChannelTypeInfo } from "../services/api";
import {
  getBrowserNotificationState,
  requestBrowserNotificationPermission,
  isChannelEnabled,
  setChannelEnabled,
} from "../services/notifications";

interface NotificationsModalProps {
  isOpen: boolean;
  onClose: () => void;
}

interface FormData {
  channel_type: string;
  display_name: string;
  config: Record<string, string>;
}

function getChannelTypes(): ChannelTypeInfo[] {
  return window.__SHELLEY_INIT__?.notification_channel_types || [];
}

const emptyForm: FormData = {
  channel_type: "",
  display_name: "",
  config: {},
};

function NotificationsModal({ isOpen, onClose }: NotificationsModalProps) {
  const { t } = useI18n();
  const [channels, setChannels] = useState<NotificationChannelAPI[]>([]);
  const [loading, setLoading] = useState(true);
  const [error, setError] = useState<string | null>(null);

  // Local channel state
  const [browserEnabled, setBrowserEnabled] = useState(() => isChannelEnabled("browser"));
  const [faviconEnabled, setFaviconEnabled] = useState(() => isChannelEnabled("favicon"));
  const [browserPermission, setBrowserPermission] = useState(getBrowserNotificationState);

  // Form state
  const [showForm, setShowForm] = useState(false);
  const [editingChannelId, setEditingChannelId] = useState<string | null>(null);
  const [form, setForm] = useState<FormData>(emptyForm);

  // Test state
  const [testing, setTesting] = useState(false);
  const [testResult, setTestResult] = useState<{ success: boolean; message: string } | null>(null);

  const channelTypes = getChannelTypes();

  const loadChannels = useCallback(async () => {
    try {
      setLoading(true);
      setError(null);
      const result = await notificationChannelsApi.getChannels();
      setChannels(result);
    } catch (err) {
      setError(err instanceof Error ? err.message : "Failed to load channels");
    } finally {
      setLoading(false);
    }
  }, []);

  useEffect(() => {
    if (isOpen) {
      loadChannels();
      setBrowserPermission(getBrowserNotificationState());
      setBrowserEnabled(isChannelEnabled("browser"));
      setFaviconEnabled(isChannelEnabled("favicon"));
    }
  }, [isOpen, loadChannels]);

  const handleEdit = (ch: NotificationChannelAPI) => {
    const configStrings: Record<string, string> = {};
    if (ch.config && typeof ch.config === "object") {
      for (const [k, v] of Object.entries(ch.config)) {
        configStrings[k] = String(v);
      }
    }
    setForm({
      channel_type: ch.channel_type,
      display_name: ch.display_name,
      config: configStrings,
    });
    setEditingChannelId(ch.channel_id);
    setTestResult(null);
    setShowForm(true);
  };

  const defaultConfigFor = (typeName: string): Record<string, string> => {
    const info = getTypeInfo(typeName);
    const config: Record<string, string> = {};
    for (const field of info?.config_fields || []) {
      if (field.default) config[field.name] = field.default;
    }
    return config;
  };

  const handleAdd = () => {
    const defaultType = channelTypes.length > 0 ? channelTypes[0].type : "";
    setForm({ ...emptyForm, channel_type: defaultType, config: defaultConfigFor(defaultType) });
    setEditingChannelId(null);
    setTestResult(null);
    setShowForm(true);
  };

  const handleCancel = () => {
    setShowForm(false);
    setEditingChannelId(null);
    setForm(emptyForm);
    setTestResult(null);
  };

  const handleSave = async () => {
    try {
      setError(null);
      if (editingChannelId) {
        const existing = channels.find((c) => c.channel_id === editingChannelId);
        await notificationChannelsApi.updateChannel(editingChannelId, {
          display_name: form.display_name,
          enabled: existing?.enabled ?? true,
          config: form.config,
        });
      } else {
        await notificationChannelsApi.createChannel({
          channel_type: form.channel_type,
          display_name: form.display_name,
          enabled: true,
          config: form.config,
        });
      }
      setShowForm(false);
      setEditingChannelId(null);
      setForm(emptyForm);
      setTestResult(null);
      await loadChannels();
    } catch (err) {
      setError(err instanceof Error ? err.message : "Failed to save channel");
    }
  };

  const handleDelete = async (channelId: string) => {
    try {
      setError(null);
      await notificationChannelsApi.deleteChannel(channelId);
      await loadChannels();
    } catch (err) {
      setError(err instanceof Error ? err.message : "Failed to delete channel");
    }
  };

  const handleToggleEnabled = async (ch: NotificationChannelAPI) => {
    try {
      setError(null);
      const configObj: Record<string, string> =
        ch.config && typeof ch.config === "object" ? (ch.config as Record<string, string>) : {};
      await notificationChannelsApi.updateChannel(ch.channel_id, {
        display_name: ch.display_name,
        enabled: !ch.enabled,
        config: configObj,
      });
      await loadChannels();
    } catch (err) {
      setError(err instanceof Error ? err.message : "Failed to update channel");
    }
  };

  const handleTest = async (channelId: string) => {
    try {
      setTesting(true);
      setTestResult(null);
      const result = await notificationChannelsApi.testChannel(channelId);
      setTestResult(result);
    } catch (err) {
      setTestResult({
        success: false,
        message: err instanceof Error ? err.message : "Test failed",
      });
    } finally {
      setTesting(false);
    }
  };

  const getTypeInfo = (typeName: string): ChannelTypeInfo | undefined => {
    return channelTypes.find((t) => t.type === typeName);
  };

  const getTypeLabel = (typeName: string): string => {
    return getTypeInfo(typeName)?.label || typeName;
  };

  // Form view
  if (showForm) {
    const typeInfo = getTypeInfo(form.channel_type);
    const configFields = typeInfo?.config_fields || [];
    const canSave = form.display_name.trim() !== "" && form.channel_type !== "";

    return (
      <Modal
        isOpen={isOpen}
        onClose={onClose}
        title={editingChannelId ? t("editChannel") : t("addChannel")}
        className="modal-wide"
      >
        {error && <div className="test-result error notifications-error-message">{error}</div>}

        {!editingChannelId && channelTypes.length > 1 && (
          <div className="form-group">
            <label>{t("channelType")}</label>
            <div className="notifications-type-selector">
              {channelTypes.map((ct) => (
                <button
                  key={ct.type}
                  className={`provider-btn${form.channel_type === ct.type ? " selected" : ""}`}
                  onClick={() =>
                    setForm({ ...form, channel_type: ct.type, config: defaultConfigFor(ct.type) })
                  }
                >
                  {ct.label}
                </button>
              ))}
            </div>
          </div>
        )}

        <div className="form-group">
          <label>{t("displayName")}</label>
          <input
            className="form-input"
            value={form.display_name}
            onChange={(e) => setForm({ ...form, display_name: e.target.value })}
            placeholder={getTypeLabel(form.channel_type)}
          />
        </div>

        {configFields.map((field) => (
          <ConfigFieldInput
            key={field.name}
            field={field}
            value={form.config[field.name] || ""}
            onChange={(val) => setForm({ ...form, config: { ...form.config, [field.name]: val } })}
          />
        ))}

        {testResult && (
          <div className={`test-result ${testResult.success ? "success" : "error"}`}>
            {testResult.message}
          </div>
        )}

        <div className="form-actions">
          <button className="btn btn-secondary" onClick={handleCancel}>
            {t("cancel")}
          </button>
          {editingChannelId && (
            <button
              className="btn btn-secondary"
              onClick={() => handleTest(editingChannelId)}
              disabled={testing}
            >
              {testing ? t("testingButton") : t("testButton")}
            </button>
          )}
          <button className="btn btn-primary" onClick={handleSave} disabled={!canSave}>
            {editingChannelId ? t("save") : t("addChannel")}
          </button>
        </div>
      </Modal>
    );
  }

  // List view
  return (
    <Modal
      isOpen={isOpen}
      onClose={onClose}
      title={t("notifications")}
      className="modal-wide"
      titleRight={
        channelTypes.length > 0 ? (
          <button className="btn btn-primary btn-sm" onClick={handleAdd}>
            + {t("addChannel")}
          </button>
        ) : undefined
      }
    >
      {error && <div className="test-result error notifications-error-message">{error}</div>}

      {/* Local channels section */}
      <div className="notifications-section">
        <div className="overflow-menu-label notifications-section-label">Local</div>

        {/* Browser notifications */}
        {typeof Notification !== "undefined" && (
          <div className="model-card notifications-card">
            <div>
              <div className="notifications-card-title">{t("browserNotifications")}</div>
              <div className="notifications-card-description">
                {browserPermission === "denied"
                  ? t("blockedByBrowser")
                  : browserPermission === "granted"
                    ? t("osNotificationsWhenHidden")
                    : t("requiresBrowserPermission")}
              </div>
            </div>
            <div className="notifications-card-actions">
              {browserPermission === "default" && !browserEnabled && (
                <button
                  className="btn btn-secondary btn-sm"
                  onClick={async () => {
                    const granted = await requestBrowserNotificationPermission();
                    setBrowserPermission(getBrowserNotificationState());
                    if (granted) setBrowserEnabled(true);
                  }}
                >
                  Enable
                </button>
              )}
              {browserPermission === "granted" && (
                <button
                  className={`btn btn-sm ${browserEnabled ? "btn-primary" : "btn-secondary"}`}
                  onClick={() => {
                    const newVal = !browserEnabled;
                    setChannelEnabled("browser", newVal);
                    setBrowserEnabled(newVal);
                  }}
                >
                  {browserEnabled ? t("on") : t("off")}
                </button>
              )}
              {browserPermission === "denied" && (
                <span className="notifications-denied-text">{t("denied")}</span>
              )}
            </div>
          </div>
        )}

        {/* Favicon */}
        <div className="model-card notifications-card">
          <div>
            <div className="notifications-card-title">{t("faviconBadge")}</div>
            <div className="notifications-card-description">
              Tab icon changes when agent finishes
            </div>
          </div>
          <button
            className={`btn btn-sm ${faviconEnabled ? "btn-primary" : "btn-secondary"}`}
            onClick={() => {
              const newVal = !faviconEnabled;
              setChannelEnabled("favicon", newVal);
              setFaviconEnabled(newVal);
            }}
          >
            {faviconEnabled ? t("on") : t("off")}
          </button>
        </div>
      </div>

      {/* Backend channels section */}
      <div>
        <div className="overflow-menu-label notifications-section-label">Server</div>

        {loading && <div className="notifications-loading">Loading...</div>}

        {!loading && channels.length === 0 && (
          <div className="notifications-empty-state">
            {t("noServerChannelsConfigured")}
            {channelTypes.length > 0 && (
              <>
                {" "}
                <button className="btn-link notifications-link-button" onClick={handleAdd}>
                  {t("addOne")}
                </button>
              </>
            )}
          </div>
        )}

        {channels.map((ch) => (
          <div key={ch.channel_id} className="model-card notifications-card">
            <div className="notifications-channel-content">
              <div className="notifications-channel-header">
                <span className="notifications-channel-name">{ch.display_name}</span>
                <span className="notifications-channel-type-badge">
                  {getTypeLabel(ch.channel_type)}
                </span>
              </div>
            </div>
            <div className="notifications-channel-actions">
              <button
                className={`btn btn-sm ${ch.enabled ? "btn-primary" : "btn-secondary"}`}
                onClick={() => handleToggleEnabled(ch)}
              >
                {ch.enabled ? t("on") : t("off")}
              </button>
              <button className="btn btn-secondary btn-sm" onClick={() => handleEdit(ch)}>
                {t("edit")}
              </button>
              <button
                className="btn btn-secondary btn-sm"
                onClick={() => handleDelete(ch.channel_id)}
              >
                <svg width="14" height="14" fill="none" stroke="currentColor" viewBox="0 0 24 24">
                  <path
                    strokeLinecap="round"
                    strokeLinejoin="round"
                    strokeWidth={2}
                    d="M19 7l-.867 12.142A2 2 0 0116.138 21H7.862a2 2 0 01-1.995-1.858L5 7m5 4v6m4-6v6m1-10V4a1 1 0 00-1-1h-4a1 1 0 00-1 1v3M4 7h16"
                  />
                </svg>
              </button>
            </div>
          </div>
        ))}
      </div>
    </Modal>
  );
}

export default NotificationsModal;
