<!-- Sign in with ChatGPT: connects ChatGPT accounts whose plans Shelley may
     use, following OpenAI's Sign in with ChatGPT UI guidelines. -->
<template>
  <section
    v-if="enabled && (unlisted.length > 0 || (message && !hideMessage))"
    class="chatgpt-plan"
    data-testid="chatgpt-plan"
  >
    <ul v-if="unlisted.length > 0" class="chatgpt-plan-accounts">
      <li
        v-for="account in unlisted"
        :key="account.id"
        :data-testid="`chatgpt-account-${account.id}`"
      >
        <div class="chatgpt-plan-account">
          <ChatGPTLogo :size="16" class="chatgpt-plan-account-logo" />
          <span class="chatgpt-plan-account-label" :title="account.name">{{ account.label }}</span>
          <span
            class="chatgpt-plan-status"
            :class="{
              'chatgpt-plan-status-on': account.signed_in && account.plan_usage,
              'chatgpt-plan-status-off': !account.signed_in || !account.plan_usage,
            }"
            >{{
              !account.signed_in
                ? t("chatgptSignedOut")
                : account.plan_usage
                  ? t("chatgptUsingPlan")
                  : t("chatgptPlanUsageOff")
            }}</span
          >
          <a
            v-if="account.signed_in && account.plan_usage"
            class="chatgpt-plan-link"
            :href="usageURL"
            target="_blank"
            rel="noopener noreferrer"
            >{{ t("chatgptManageUsage") }} ↗</a
          >
        </div>
        <div class="chatgpt-plan-actions">
          <Button
            v-if="account.signed_in && !account.plan_usage"
            size="small"
            :label="t('chatgptEnablePlanUsage')"
            :disabled="busy"
            @click="startSignIn(account.id, true)"
          />
          <Button
            v-if="!account.signed_in"
            size="small"
            :label="t('chatgptSignInAgain')"
            :disabled="busy"
            @click="startSignIn(account.id, false)"
          />
          <Button
            v-if="account.signed_in"
            size="small"
            severity="secondary"
            text
            :label="t('chatgptSignOut')"
            :disabled="busy"
            @click="signOut(account, false)"
          />
          <Button
            v-else
            size="small"
            severity="danger"
            text
            :label="t('chatgptRemoveAccount')"
            :disabled="busy"
            @click="signOut(account, true)"
          />
        </div>
      </li>
    </ul>

    <div v-if="message && !hideMessage" class="chatgpt-plan-message" role="alert">{{ message }}</div>

  </section>

  <Modal :is-open="welcome" :title="t('chatgptWelcomeTitle')" @close="welcome = false">
    <div class="chatgpt-welcome">
      <ChatGPTLogo :size="48" />
      <p>{{ t("chatgptWelcomeBody") }}</p>
      <div class="form-actions">
        <a class="chatgpt-plan-link" :href="usageURL" target="_blank" rel="noopener noreferrer"
          >{{ t("chatgptManageUsage") }} ↗</a
        >
        <Button :label="t('chatgptGotIt')" @click="welcome = false" />
      </div>
    </div>
  </Modal>
</template>

<script setup lang="ts">
import { computed, onBeforeUnmount, onMounted, ref } from "vue";
import Button from "primevue/button";
import Modal from "./Modal.vue";
import ChatGPTLogo from "./ChatGPTLogo.vue";
import { useI18n } from "../composables/i18n";
import {
  chatgptApi,
  type ChatGPTAccount,
  type ChatGPTSignIn,
  type ChatGPTSignInResult,
} from "../../services/api";
import { CHATGPT_USAGE_URL } from "./chatgptPlan";

// listed holds accounts whose plan models Manage models lists; their
// status and actions live in the table's group header instead.
// Sign-ins run in the Add Models dialog, which shows their progress; while
// it is open it shows their errors too (hideMessage).
const props = defineProps<{ listed: string[]; hideMessage?: boolean }>();
const emit = defineEmits<{ (e: "modelsChanged"): void; (e: "signedIn"): void }>();
const { t } = useI18n();

const enabled = ref(false);
const accounts = ref<ChatGPTAccount[]>([]);
const usageURL = CHATGPT_USAGE_URL;
const pending = ref<ChatGPTSignIn | null>(null);
const busy = ref(false);
const message = ref<string | null>(null);
const welcome = ref(false);
const unlisted = computed(() =>
  accounts.value.filter((a) => !(a.signed_in && a.plan_usage && props.listed.includes(a.id))),
);
let poll: ReturnType<typeof setInterval> | null = null;

// Server messages are sentences without a leading capital.
function describe(err: unknown): string {
  const text = err instanceof Error ? err.message : String(err);
  return text.charAt(0).toUpperCase() + text.slice(1);
}

async function load() {
  try {
    const result = await chatgptApi.accounts();
    enabled.value = result.enabled;
    accounts.value = result.accounts;
  } catch (err) {
    message.value = describe(err);
  }
}

function stopPolling() {
  if (poll) clearInterval(poll);
  poll = null;
}

function cancel() {
  stopPolling();
  pending.value = null;
}

// failed shows a sign-in error. The sign-in may still have changed the
// account (signed in, but listing its models failed), so reload it.
async function failed(err: unknown) {
  const text = describe(err);
  await load();
  emit("modelsChanged");
  message.value = text;
}

async function finished(result: ChatGPTSignInResult) {
  cancel();
  message.value = null;
  await load();
  emit("modelsChanged");
  emit("signedIn");
  if (result.new_registration && result.account.plan_usage) welcome.value = true;
}

async function startSignIn(accountId: string | undefined, consent: boolean) {
  message.value = null;
  // Open the tab inside the click so popup blockers allow it.
  const tab = window.open("about:blank", "_blank");
  busy.value = true;
  try {
    const signIn = await chatgptApi.startSignIn(accountId, consent);
    pending.value = signIn;
    if (tab) {
      tab.opener = null;
      tab.location.href = signIn.authorize_url;
    }
    stopPolling();
    // The loopback listener finishes on its own when the browser runs here.
    poll = setInterval(async () => {
      if (!pending.value || pending.value.id !== signIn.id) return;
      try {
        const status = await chatgptApi.status(signIn.id);
        if (!status.done || pending.value?.id !== signIn.id) return;
        if (status.result) await finished(status.result);
        else {
          cancel();
          if (status.error) await failed(status.error);
        }
      } catch {
        // transient; keep waiting
      }
    }, 2000);
  } catch (err) {
    tab?.close();
    message.value = describe(err);
  } finally {
    busy.value = false;
  }
}

async function complete(url: string) {
  busy.value = true;
  message.value = null;
  try {
    await finished(await chatgptApi.complete(url));
  } catch (err) {
    await failed(err);
  } finally {
    busy.value = false;
  }
}

async function signOut(account: ChatGPTAccount, remove: boolean) {
  busy.value = true;
  message.value = null;
  try {
    const result = await chatgptApi.signOut(account.id, remove);
    if (result.warning) message.value = t("chatgptSignOutUnconfirmed");
  } catch (err) {
    message.value = describe(err);
  } finally {
    busy.value = false;
    await load();
    emit("modelsChanged");
  }
}

// Add Models starts sign-ins and shows their progress; Manage models puts
// listed accounts' actions in its table.
defineExpose({
  enabled,
  accounts,
  busy,
  pending,
  message,
  signIn: () => startSignIn(undefined, false),
  complete,
  cancel,
  signOut: (account: ChatGPTAccount) => signOut(account, false),
});

onMounted(load);
onBeforeUnmount(stopPolling);
</script>

<style scoped>
.chatgpt-plan {
  display: flex;
  flex-direction: column;
  gap: 0.6rem;
  padding: 0.85rem 1rem;
  border-bottom: 1px solid var(--border);
}
.chatgpt-plan-accounts {
  list-style: none;
  margin: 0;
  padding: 0;
  display: flex;
  flex-direction: column;
  gap: 0.35rem;
}
.chatgpt-plan-accounts li {
  display: flex;
  flex-wrap: wrap;
  align-items: center;
  justify-content: space-between;
  gap: 0.5rem;
}
.chatgpt-plan-account {
  display: flex;
  flex-wrap: wrap;
  align-items: baseline;
  gap: 0.6rem;
  min-width: 0;
}
.chatgpt-plan-account-logo {
  align-self: center;
}
.chatgpt-plan-account-label {
  font-weight: 500;
  overflow-wrap: anywhere;
}
.chatgpt-plan-status {
  font-size: 0.8rem;
}
.chatgpt-plan-status-on {
  color: var(--success-text);
}
.chatgpt-plan-status-off {
  color: var(--text-secondary);
}
.chatgpt-plan-link {
  font-size: 0.85rem;
  color: var(--link-color);
}
.chatgpt-plan-actions {
  display: flex;
  gap: 0.25rem;
}
.chatgpt-plan-message {
  padding: 0.5rem 0.75rem;
  border: 1px solid var(--error-border);
  border-radius: 6px;
  background: var(--error-bg);
  color: var(--error-text);
  font-size: 0.85rem;
}
.chatgpt-welcome {
  display: flex;
  flex-direction: column;
  align-items: center;
  gap: 0.75rem;
  text-align: center;
  padding: 0.5rem 0.5rem 0;
}
.chatgpt-welcome .form-actions {
  display: flex;
  align-items: center;
  gap: 1rem;
}
</style>
