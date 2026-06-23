import React, { useState, useEffect, useCallback } from "react";
import { PlusIcon, Trash2Icon } from "lucide-react";
import Modal from "./Modal";
import { useI18n } from "../i18n";
import ConfigFieldInput from "./ConfigFieldInput";
import { Button } from "@/components/ui/button";
import { Input } from "@/components/ui/input";
import { Label } from "@/components/ui/label";
import { Badge } from "@/components/ui/badge";
import { cn } from "@/lib/utils";
import {
  api,
  notificationChannelsApi,
  NotificationChannelAPI,
  ChannelTypeInfo,
} from "../services/api";
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

const sectionLabelClass =
  "mb-2 text-xs font-medium tracking-wide text-muted-foreground uppercase";

const cardClass =
  "mb-2 flex items-center justify-between gap-3 rounded-lg border border-border bg-card px-4 py-3 text-card-foreground";

function NotificationsModal({ isOpen, onClose }: NotificationsModalProps) {
  const { t } = useI18n();
  const [channels, setChannels] = useState<NotificationChannelAPI[]>([]);
  const [loading, setLoading] = useState(true);
  const [error, setError] = useState<string | null>(null);

  // Local channel state
  const [browserEnabled, setBrowserEnabled] = useState(() => isChannelEnabled("browser"));
  const [faviconEnabled, setFaviconEnabled] = useState(() => isChannelEnabled("favicon"));
  const [browserPermission, setBrowserPermission] = useState(getBrowserNotificationState);

  // exe.dev push notifications (auto-configured when the VM has a "notify"
  // integration). Enabled by default; the user can turn it off here.
  const exeNotifyAvailable = window.__SHELLEY_INIT__?.exe_notify_available ?? false;
  const [exeNotifyEnabled, setExeNotifyEnabled] = useState(true);

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
      if (exeNotifyAvailable) {
        api
          .getSettings()
          .then((settings) => setExeNotifyEnabled(settings.exe_notify !== "false"))
          .catch(() => {});
      }
    }
  }, [isOpen, loadChannels, exeNotifyAvailable]);

  const handleToggleExeNotify = async () => {
    const newVal = !exeNotifyEnabled;
    setExeNotifyEnabled(newVal);
    try {
      setError(null);
      await api.setSetting("exe_notify", newVal ? "true" : "false");
    } catch (err) {
      setExeNotifyEnabled(!newVal);
      setError(err instanceof Error ? err.message : "Failed to update setting");
    }
  };

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
        className="sm:max-w-2xl"
      >
        {error && (
          <div className="mb-4 rounded-md border border-destructive/30 bg-destructive/10 px-3 py-2 text-sm text-destructive">
            {error}
          </div>
        )}

        {!editingChannelId && channelTypes.length > 1 && (
          <div className="mb-3 flex flex-col gap-1.5">
            <Label>{t("channelType")}</Label>
            <div className="flex flex-wrap gap-2">
              {channelTypes.map((ct) => (
                <Button
                  key={ct.type}
                  type="button"
                  size="sm"
                  variant={form.channel_type === ct.type ? "default" : "outline"}
                  onClick={() =>
                    setForm({ ...form, channel_type: ct.type, config: defaultConfigFor(ct.type) })
                  }
                >
                  {ct.label}
                </Button>
              ))}
            </div>
          </div>
        )}

        <div className="mb-3 flex flex-col gap-1.5">
          <Label htmlFor="notifications-display-name">{t("displayName")}</Label>
          <Input
            id="notifications-display-name"
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
          <div
            className={cn(
              "mt-3 rounded-md border px-3 py-2 text-sm",
              testResult.success
                ? "border-emerald-600/30 bg-emerald-600/10 text-emerald-700 dark:text-emerald-400"
                : "border-destructive/30 bg-destructive/10 text-destructive"
            )}
          >
            {testResult.message}
          </div>
        )}

        <div className="mt-5 flex justify-end gap-2">
          <Button variant="outline" onClick={handleCancel}>
            {t("cancel")}
          </Button>
          {editingChannelId && (
            <Button
              variant="outline"
              onClick={() => handleTest(editingChannelId)}
              disabled={testing}
            >
              {testing ? t("testingButton") : t("testButton")}
            </Button>
          )}
          <Button onClick={handleSave} disabled={!canSave}>
            {editingChannelId ? t("save") : t("addChannel")}
          </Button>
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
      className="sm:max-w-2xl"
      titleRight={
        channelTypes.length > 0 ? (
          <Button size="sm" onClick={handleAdd}>
            <PlusIcon />
            {t("addChannel")}
          </Button>
        ) : undefined
      }
    >
      {error && (
        <div className="mb-4 rounded-md border border-destructive/30 bg-destructive/10 px-3 py-2 text-sm text-destructive">
          {error}
        </div>
      )}

      {/* Local channels section */}
      <div className="mb-5">
        <div className={sectionLabelClass}>Local</div>

        {/* Browser notifications */}
        {typeof Notification !== "undefined" && (
          <div className={cardClass}>
            <div className="min-w-0">
              <div className="text-sm font-medium">{t("browserNotifications")}</div>
              <div className="text-xs text-muted-foreground">
                {browserPermission === "denied"
                  ? t("blockedByBrowser")
                  : browserPermission === "granted"
                    ? t("osNotificationsWhenHidden")
                    : t("requiresBrowserPermission")}
              </div>
            </div>
            <div className="flex shrink-0 items-center gap-2">
              {browserPermission === "default" && !browserEnabled && (
                <Button
                  size="sm"
                  variant="outline"
                  onClick={async () => {
                    const granted = await requestBrowserNotificationPermission();
                    setBrowserPermission(getBrowserNotificationState());
                    if (granted) setBrowserEnabled(true);
                  }}
                >
                  Enable
                </Button>
              )}
              {browserPermission === "granted" && (
                <Button
                  size="sm"
                  variant={browserEnabled ? "default" : "outline"}
                  onClick={() => {
                    const newVal = !browserEnabled;
                    setChannelEnabled("browser", newVal);
                    setBrowserEnabled(newVal);
                  }}
                >
                  {browserEnabled ? t("on") : t("off")}
                </Button>
              )}
              {browserPermission === "denied" && (
                <span className="text-xs text-muted-foreground">{t("denied")}</span>
              )}
            </div>
          </div>
        )}

        {/* exe.dev push notifications (auto-configured) */}
        {exeNotifyAvailable && (
          <div className={cardClass}>
            <div className="min-w-0">
              <div className="text-sm font-medium">{t("exeDevPushNotifications")}</div>
              <div className="text-xs text-muted-foreground">
                {t("exeDevPushNotificationsDescription")}
              </div>
            </div>
            <Button
              size="sm"
              variant={exeNotifyEnabled ? "default" : "outline"}
              onClick={handleToggleExeNotify}
            >
              {exeNotifyEnabled ? t("on") : t("off")}
            </Button>
          </div>
        )}

        {/* Favicon */}
        <div className={cardClass}>
          <div className="min-w-0">
            <div className="text-sm font-medium">{t("faviconBadge")}</div>
            <div className="text-xs text-muted-foreground">
              Tab icon changes when agent finishes
            </div>
          </div>
          <Button
            size="sm"
            variant={faviconEnabled ? "default" : "outline"}
            onClick={() => {
              const newVal = !faviconEnabled;
              setChannelEnabled("favicon", newVal);
              setFaviconEnabled(newVal);
            }}
          >
            {faviconEnabled ? t("on") : t("off")}
          </Button>
        </div>
      </div>

      {/* Backend channels section */}
      <div>
        <div className={sectionLabelClass}>Server</div>

        {loading && <div className="py-4 text-sm text-muted-foreground">Loading...</div>}

        {!loading && channels.length === 0 && (
          <div className="py-4 text-sm text-muted-foreground">
            {t("noServerChannelsConfigured")}
            {channelTypes.length > 0 && (
              <>
                {" "}
                <Button variant="link" className="h-auto p-0" onClick={handleAdd}>
                  {t("addOne")}
                </Button>
              </>
            )}
          </div>
        )}

        {channels.map((ch) => (
          <div key={ch.channel_id} className={cardClass}>
            <div className="min-w-0 flex-1">
              <div className="flex items-center gap-2">
                <span className="truncate text-sm font-medium">{ch.display_name}</span>
                <Badge variant="secondary">{getTypeLabel(ch.channel_type)}</Badge>
              </div>
            </div>
            <div className="flex shrink-0 items-center gap-2">
              <Button
                size="sm"
                variant={ch.enabled ? "default" : "outline"}
                onClick={() => handleToggleEnabled(ch)}
              >
                {ch.enabled ? t("on") : t("off")}
              </Button>
              <Button size="sm" variant="outline" onClick={() => handleEdit(ch)}>
                {t("edit")}
              </Button>
              <Button
                size="icon-sm"
                variant="outline"
                aria-label={t("delete_")}
                onClick={() => handleDelete(ch.channel_id)}
              >
                <Trash2Icon />
              </Button>
            </div>
          </div>
        ))}
      </div>
    </Modal>
  );
}

export default NotificationsModal;
