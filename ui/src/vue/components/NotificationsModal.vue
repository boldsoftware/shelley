<!-- Vue port of components/NotificationsModal.tsx. Server channel CRUD/test +
     browser-notification permission + favicon/browser/exe.dev push toggles.
     Preserves the notifications-* / model-card / provider-btn / form-* /
     test-result / overflow-menu-label / btn-link class contract and all i18n
     text. Uses Modal.vue, ConfigFieldInput.vue, useI18n, and
     api/notificationChannelsApi plus services/notifications. -->
<template>
  <!-- Form view -->
  <Modal
    v-if="showForm"
    :is-open="isOpen"
    :title="editingChannelId ? t('editChannel') : t('addChannel')"
    class-name="modal-wide"
    @close="handleClose"
  >
    <div v-if="error" class="test-result error notifications-error-message">{{ error }}</div>

    <div v-if="!editingChannelId && channelTypes.length > 1" class="form-group">
      <label>{{ t("channelType") }}</label>
      <div class="notifications-type-selector">
        <button
          v-for="ct in channelTypes"
          :key="ct.type"
          :class="`provider-btn${form.channel_type === ct.type ? ' selected' : ''}`"
          @click="selectType(ct.type)"
        >
          {{ ct.label }}
        </button>
      </div>
    </div>

    <div class="form-group">
      <label>{{ t("displayName") }}</label>
      <InputText
        v-model="form.display_name"
        :placeholder="getTypeLabel(form.channel_type)"
        fluid
        :dt="inputFieldDt"
      />
    </div>

    <ConfigFieldInput
      v-for="field in configFields"
      :key="field.name"
      :field="field"
      :value="form.config[field.name] || ''"
      :saved-secret="savedSecrets.includes(field.name)"
      :clear-saved="clearedSecrets.includes(field.name)"
      @change="(val: string) => (form.config = { ...form.config, [field.name]: val })"
      @clear="(val: boolean) => setClearSecret(field.name, val)"
    />

    <div v-if="testResult" :class="`test-result ${testResult.success ? 'success' : 'error'}`">
      {{ testResult.message }}
    </div>

    <div class="form-actions">
      <Button severity="secondary" :label="t('cancel')" @click="handleCancel" />
      <Button
        v-if="editingChannelId"
        severity="secondary"
        :label="testing ? t('testingButton') : t('testButton')"
        :disabled="testing"
        @click="handleTest(editingChannelId)"
      />
      <Button
        :label="editingChannelId ? t('save') : t('addChannel')"
        :disabled="!canSave"
        @click="handleSave"
      />
    </div>
  </Modal>

  <!-- List view -->
  <Modal
    v-else
    :is-open="isOpen"
    :title="t('notifications')"
    class-name="modal-wide"
    @close="handleClose"
  >
    <template v-if="channelTypes.length > 0" #title-right>
      <Button size="small" @click="handleAdd">+ {{ t("addChannel") }}</Button>
    </template>

    <div v-if="error" class="test-result error notifications-error-message">{{ error }}</div>

    <!-- Local channels section -->
    <div class="notifications-section">
      <div class="overflow-menu-label notifications-section-label">Local</div>

      <!-- Browser notifications -->
      <div v-if="notificationSupported" class="model-card notifications-card">
        <div>
          <div class="notifications-card-title">{{ t("browserNotifications") }}</div>
          <div class="notifications-card-description">
            {{
              browserPermission === "denied"
                ? t("blockedByBrowser")
                : browserPermission === "granted"
                  ? t("osNotificationsWhenHidden")
                  : t("requiresBrowserPermission")
            }}
          </div>
        </div>
        <div class="notifications-card-actions">
          <Button
            v-if="browserPermission === 'default' && !browserEnabled"
            severity="secondary"
            size="small"
            label="Enable"
            @click="enableBrowser"
          />
          <Button
            v-if="browserPermission === 'granted'"
            size="small"
            :severity="browserEnabled ? undefined : 'secondary'"
            :label="browserEnabled ? t('on') : t('off')"
            @click="toggleBrowser"
          />
          <span v-if="browserPermission === 'denied'" class="notifications-denied-text">
            {{ t("denied") }}
          </span>
        </div>
      </div>

      <!-- exe.dev push notifications (auto-configured) -->
      <div v-if="exeNotifyAvailable" class="model-card notifications-card">
        <div>
          <div class="notifications-card-title">{{ t("exeDevPushNotifications") }}</div>
          <div class="notifications-card-description">
            {{ t("exeDevPushNotificationsDescription") }}
          </div>
        </div>
        <Button
          size="small"
          :severity="exeNotifyEnabled ? undefined : 'secondary'"
          :label="exeNotifyEnabled ? t('on') : t('off')"
          @click="handleToggleExeNotify"
        />
      </div>

      <!-- Favicon -->
      <div class="model-card notifications-card">
        <div>
          <div class="notifications-card-title">{{ t("faviconBadge") }}</div>
          <div class="notifications-card-description">Tab icon changes when agent finishes</div>
        </div>
        <Button
          size="small"
          :severity="faviconEnabled ? undefined : 'secondary'"
          :label="faviconEnabled ? t('on') : t('off')"
          @click="toggleFavicon"
        />
      </div>
    </div>

    <!-- Backend channels section -->
    <div>
      <div class="overflow-menu-label notifications-section-label">Server</div>

      <div v-if="loading" class="notifications-loading">Loading...</div>

      <div v-if="!loading && channels.length === 0" class="notifications-empty-state">
        {{ t("noServerChannelsConfigured") }}
        <template v-if="channelTypes.length > 0">
          {{ " " }}
          <Button link class="notifications-link-button" :label="t('addOne')" @click="handleAdd" />
        </template>
      </div>

      <div v-for="ch in channels" :key="ch.channel_id" class="model-card notifications-card">
        <div class="notifications-channel-content">
          <div class="notifications-channel-header">
            <span class="notifications-channel-name">{{ ch.display_name }}</span>
            <span class="notifications-channel-type-badge">{{
              getTypeLabel(ch.channel_type)
            }}</span>
          </div>
        </div>
        <div class="notifications-channel-actions">
          <Button
            size="small"
            :severity="ch.enabled ? undefined : 'secondary'"
            :label="ch.enabled ? t('on') : t('off')"
            @click="handleToggleEnabled(ch)"
          />
          <Button severity="secondary" size="small" :label="t('edit')" @click="handleEdit(ch)" />
          <Button severity="secondary" size="small" @click="handleDelete(ch.channel_id)">
            <svg width="14" height="14" fill="none" stroke="currentColor" viewBox="0 0 24 24">
              <path
                stroke-linecap="round"
                stroke-linejoin="round"
                :stroke-width="2"
                d="M19 7l-.867 12.142A2 2 0 0116.138 21H7.862a2 2 0 01-1.995-1.858L5 7m5 4v6m4-6v6m1-10V4a1 1 0 00-1-1h-4a1 1 0 00-1 1v3M4 7h16"
              />
            </svg>
          </Button>
        </div>
      </div>
    </div>
  </Modal>
</template>

<script setup lang="ts">
import { computed, reactive, ref, watch } from "vue";
import Modal from "./Modal.vue";
import Button from "primevue/button";
import InputText from "primevue/inputtext";
import { inputFieldDt } from "./configFieldDt";
import { useI18n } from "../composables/i18n";
import ConfigFieldInput from "./ConfigFieldInput.vue";
import {
  api,
  notificationChannelsApi,
  type NotificationChannelAPI,
  type ChannelTypeInfo,
} from "../../services/api";
import {
  getBrowserNotificationState,
  requestBrowserNotificationPermission,
  isChannelEnabled,
  setChannelEnabled,
} from "../../services/notifications";

interface FormData {
  channel_type: string;
  display_name: string;
  config: Record<string, string>;
}

const props = defineProps<{ isOpen: boolean }>();
const emit = defineEmits<{ (e: "close"): void }>();

const { t } = useI18n();

function getChannelTypes(): ChannelTypeInfo[] {
  return window.__SHELLEY_INIT__?.notification_channel_types || [];
}

const emptyForm: FormData = {
  channel_type: "",
  display_name: "",
  config: {},
};

const channels = ref<NotificationChannelAPI[]>([]);
const loading = ref(true);
const error = ref<string | null>(null);

const browserEnabled = ref(isChannelEnabled("browser"));
const faviconEnabled = ref(isChannelEnabled("favicon"));
const browserPermission = ref(getBrowserNotificationState());

const exeNotifyAvailable = window.__SHELLEY_INIT__?.exe_notify_available ?? false;
const exeNotifyEnabled = ref(true);

const showForm = ref(false);
const editingChannelId = ref<string | null>(null);
const form = reactive<FormData>({ ...emptyForm, config: {} });
const savedSecrets = ref<string[]>([]);
const clearedSecrets = ref<string[]>([]);

const testing = ref(false);
const testResult = ref<{ success: boolean; message: string } | null>(null);

const channelTypes = getChannelTypes();

const notificationSupported = typeof Notification !== "undefined";

const typeInfo = computed(() => getTypeInfo(form.channel_type));
const configFields = computed(() => typeInfo.value?.config_fields || []);
const canSave = computed(
  () =>
    form.display_name.trim() !== "" &&
    form.channel_type !== "" &&
    configFields.value.every(
      (field) =>
        field.type !== "password" ||
        !field.required ||
        !!form.config[field.name]?.trim() ||
        savedSecrets.value.includes(field.name),
    ),
);

function resetForm() {
  Object.assign(form, { channel_type: "", display_name: "", config: {} });
  savedSecrets.value = [];
  clearedSecrets.value = [];
}

async function loadChannels() {
  try {
    loading.value = true;
    error.value = null;
    channels.value = await notificationChannelsApi.getChannels();
  } catch (err) {
    error.value = err instanceof Error ? err.message : "Failed to load channels";
  } finally {
    loading.value = false;
  }
}

async function handleToggleExeNotify() {
  const newVal = !exeNotifyEnabled.value;
  exeNotifyEnabled.value = newVal;
  try {
    error.value = null;
    await api.setSetting("exe_notify", newVal ? "true" : "false");
  } catch (err) {
    exeNotifyEnabled.value = !newVal;
    error.value = err instanceof Error ? err.message : "Failed to update setting";
  }
}

function getTypeInfo(typeName: string): ChannelTypeInfo | undefined {
  return channelTypes.find((c) => c.type === typeName);
}

function getTypeLabel(typeName: string): string {
  return getTypeInfo(typeName)?.label || typeName;
}

function defaultConfigFor(typeName: string): Record<string, string> {
  const info = getTypeInfo(typeName);
  const config: Record<string, string> = {};
  for (const field of info?.config_fields || []) {
    if (field.type !== "password" && field.default) config[field.name] = field.default;
  }
  return config;
}

function selectType(type: string) {
  form.channel_type = type;
  form.config = defaultConfigFor(type);
  savedSecrets.value = [];
  clearedSecrets.value = [];
}

function setClearSecret(name: string, clear: boolean) {
  clearedSecrets.value = clear
    ? [...clearedSecrets.value, name]
    : clearedSecrets.value.filter((fieldName) => fieldName !== name);
  if (clear) form.config = { ...form.config, [name]: "" };
}

function handleEdit(ch: NotificationChannelAPI) {
  Object.assign(form, {
    channel_type: ch.channel_type,
    display_name: ch.display_name,
    config: { ...ch.config },
  });
  savedSecrets.value = ch.configured_secrets;
  clearedSecrets.value = [];
  editingChannelId.value = ch.channel_id;
  testResult.value = null;
  showForm.value = true;
}

function handleAdd() {
  resetForm();
  const defaultType = channelTypes.length > 0 ? channelTypes[0].type : "";
  Object.assign(form, {
    channel_type: defaultType,
    display_name: "",
    config: defaultConfigFor(defaultType),
  });
  editingChannelId.value = null;
  testResult.value = null;
  showForm.value = true;
}

function handleCancel() {
  showForm.value = false;
  editingChannelId.value = null;
  resetForm();
  testResult.value = null;
  error.value = null;
}

function handleClose() {
  handleCancel();
  emit("close");
}

async function handleSave() {
  try {
    error.value = null;
    const config = { ...form.config };
    for (const field of configFields.value) {
      if (field.type !== "password") continue;
      if (clearedSecrets.value.includes(field.name)) {
        config[field.name] = "";
      } else if (!config[field.name]) {
        delete config[field.name];
      }
    }
    if (editingChannelId.value) {
      const existing = channels.value.find((c) => c.channel_id === editingChannelId.value);
      if (!existing) throw new Error("Channel not found");
      await notificationChannelsApi.updateChannel(editingChannelId.value, {
        display_name: form.display_name,
        enabled: existing.enabled,
        config,
      });
    } else {
      await notificationChannelsApi.createChannel({
        channel_type: form.channel_type,
        display_name: form.display_name,
        enabled: true,
        config,
      });
    }
    handleCancel();
    await loadChannels();
  } catch (err) {
    error.value = err instanceof Error ? err.message : "Failed to save channel";
  }
}

async function handleDelete(channelId: string) {
  try {
    error.value = null;
    await notificationChannelsApi.deleteChannel(channelId);
    await loadChannels();
  } catch (err) {
    error.value = err instanceof Error ? err.message : "Failed to delete channel";
  }
}

async function handleToggleEnabled(ch: NotificationChannelAPI) {
  try {
    error.value = null;
    await notificationChannelsApi.updateChannel(ch.channel_id, {
      display_name: ch.display_name,
      enabled: !ch.enabled,
      config: ch.config,
    });
    await loadChannels();
  } catch (err) {
    error.value = err instanceof Error ? err.message : "Failed to update channel";
  }
}

async function handleTest(channelId: string) {
  try {
    testing.value = true;
    testResult.value = null;
    testResult.value = await notificationChannelsApi.testChannel(channelId);
  } catch (err) {
    testResult.value = {
      success: false,
      message: err instanceof Error ? err.message : "Test failed",
    };
  } finally {
    testing.value = false;
  }
}

async function enableBrowser() {
  const granted = await requestBrowserNotificationPermission();
  browserPermission.value = getBrowserNotificationState();
  if (granted) browserEnabled.value = true;
}

function toggleBrowser() {
  const newVal = !browserEnabled.value;
  setChannelEnabled("browser", newVal);
  browserEnabled.value = newVal;
}

function toggleFavicon() {
  const newVal = !faviconEnabled.value;
  setChannelEnabled("favicon", newVal);
  faviconEnabled.value = newVal;
}

watch(
  () => props.isOpen,
  (open) => {
    if (open) {
      loadChannels();
      browserPermission.value = getBrowserNotificationState();
      browserEnabled.value = isChannelEnabled("browser");
      faviconEnabled.value = isChannelEnabled("favicon");
      if (exeNotifyAvailable) {
        api
          .getSettings()
          .then((settings) => (exeNotifyEnabled.value = settings.exe_notify !== "false"))
          .catch(() => {});
      }
    } else {
      handleCancel();
    }
  },
  { immediate: true },
);
</script>
