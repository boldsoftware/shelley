<!-- A VM's settings, its view's Settings tab: exe.dev's VM details, read and
     changed through the exe.dev integration that lists the VM. Every change
     is an account action, so each opens a dialog saying what it does, to be
     confirmed (deleting by typing the VM's name). What the integration may
     not change is disabled, and says why. -->
<template>
  <section class="project-page" aria-label="VM settings" data-testid="vm-settings">
    <div class="project-page-body">
      <p v-if="loadError" class="error-line project-page-form" role="alert">{{ loadError }}</p>
      <p v-else-if="!vm" class="text-secondary project-page-form">Loading…</p>
      <div v-else class="vm-card">
        <div class="vm-header">
          <span class="vm-emoji" aria-hidden="true">{{ vm.emoji }}</span>
          <h2 class="vm-name">{{ vm.vm_name }}</h2>
          <span v-tooltip.top="why('rename')">
            <button
              type="button"
              class="vm-title-btn"
              :aria-label="`Rename ${name}`"
              :disabled="!!why('rename')"
              data-testid="vm-rename"
              @click="rename"
            >
              <i class="pi pi-pencil" aria-hidden="true" />
            </button>
          </span>
          <span v-if="isPublic" class="vm-badge-public">PUBLIC</span>
          <div class="vm-header-actions">
            <span
              v-for="action in headerActions"
              :key="action.kind"
              v-tooltip.top="why(action.kind)"
            >
              <button
                type="button"
                class="vm-header-btn"
                :class="{ danger: action.kind === 'delete' }"
                :disabled="!!why(action.kind)"
                :data-testid="`vm-${action.kind}`"
                @click="action.run"
              >
                <i :class="`pi ${action.icon}`" aria-hidden="true" />
                {{ action.label }}
              </button>
            </span>
          </div>
        </div>

        <div class="vm-links">
          <span
            v-if="vm.ssh_command"
            class="vm-ssh"
            :class="{ copied }"
            @click="copyUnlessSelecting"
          >
            <span class="vm-ssh-prompt" aria-hidden="true">$</span>
            <code>{{ vm.ssh_command }}</code>
            <button
              type="button"
              class="vm-ssh-btn"
              :aria-label="copied ? 'Copied' : 'Copy SSH command'"
              :title="copied ? 'Copied!' : 'Copy SSH command'"
              @click.stop="copySsh"
            >
              <i :class="copied ? 'pi pi-check' : 'pi pi-copy'" aria-hidden="true" />
            </button>
          </span>
          <div class="vm-link-btns">
            <a :href="vm.https_url" class="vm-link" target="_blank" rel="noopener"
              ><i class="pi pi-globe" aria-hidden="true" /> HTTPS</a
            >
            <a
              v-if="vm.terminal_url"
              :href="vm.terminal_url"
              class="vm-link"
              target="_blank"
              rel="noopener"
              ><i class="pi pi-chevron-right" aria-hidden="true" /> Terminal</a
            >
            <a
              v-if="vm.shelley_url"
              :href="vm.shelley_url"
              class="vm-link"
              target="_blank"
              rel="noopener"
              ><i class="pi pi-sparkles" aria-hidden="true" /> Shelley</a
            >
          </div>
        </div>

        <div class="vm-grid">
          <dl class="vm-col">
            <dt>Status:</dt>
            <dd data-testid="vm-status">{{ vm.status }}</dd>
            <dt>Size:</dt>
            <dd>
              {{ size }}
              <span v-tooltip.top="why('resize')">
                <button
                  type="button"
                  class="vm-action-btn"
                  :disabled="!!why('resize')"
                  data-testid="vm-resize"
                  @click="openResize"
                >
                  <i class="pi pi-arrows-alt" aria-hidden="true" /> Resize
                </button>
              </span>
            </dd>
            <template v-if="vm.region_display">
              <dt>Region:</dt>
              <dd>{{ vm.region_display }}</dd>
            </template>
            <template v-if="vm.image">
              <dt>Image:</dt>
              <dd>{{ vm.image }}</dd>
            </template>
            <template v-if="vm.pool_name">
              <dt>Pool:</dt>
              <dd>⛁ {{ vm.pool_name }}</dd>
            </template>
            <template v-if="vm.created_at">
              <dt>Created:</dt>
              <dd>{{ created }}</dd>
            </template>
            <dt>Comment:</dt>
            <dd>
              <span :class="vm.comment ? 'vm-comment' : 'vm-empty'" data-testid="vm-comment">{{
                vm.comment || "—"
              }}</span>
              <span v-tooltip.top="why('comment')">
                <button
                  type="button"
                  class="vm-detail-btn"
                  :disabled="!!why('comment')"
                  data-testid="vm-comment-edit"
                  @click="editComment"
                >
                  {{ vm.comment ? "Edit" : "Add" }}
                </button>
              </span>
            </dd>
          </dl>

          <dl class="vm-col vm-access">
            <dt>Tags:</dt>
            <dd class="vm-chips" data-testid="vm-tags">
              <span v-for="tag in vm.tags" :key="tag" class="vm-tag">
                #{{ tag }}
                <button
                  v-if="!why('tag-remove')"
                  type="button"
                  class="vm-chip-remove"
                  :aria-label="`Remove tag ${tag}`"
                  @click="removeTag(tag)"
                >
                  ×
                </button>
              </span>
            </dd>
            <span v-tooltip.top="why('tag-add')">
              <button
                type="button"
                class="vm-detail-btn"
                :disabled="!!why('tag-add')"
                @click="addTag"
              >
                Add Tag
              </button>
            </span>

            <dt>Web:</dt>
            <dd>Port {{ vm.proxy_port }}</dd>
            <span v-tooltip.top="why('port')">
              <button
                type="button"
                class="vm-detail-btn"
                :disabled="!!why('port')"
                @click="changePort"
              >
                Change
              </button>
            </span>

            <dt>Domains:</dt>
            <dd class="vm-chips" data-testid="vm-domains">
              <span v-if="!vm.domains?.length" class="vm-empty">—</span>
              <span v-for="domain in vm.domains ?? []" :key="domain" class="vm-domain">
                <a :href="`https://${domain}/`" target="_blank" rel="noopener">{{ domain }}</a>
                <button
                  v-if="!why('domain-remove')"
                  type="button"
                  class="vm-chip-remove"
                  :aria-label="`Remove domain ${domain}`"
                  @click="domainRemove(domain)"
                >
                  ×
                </button>
              </span>
            </dd>
            <span v-tooltip.top="why('domain-add')">
              <button
                type="button"
                class="vm-detail-btn"
                :disabled="!!why('domain-add')"
                data-testid="vm-domain-add"
                @click="domainAdd"
              >
                Add Domain
              </button>
            </span>

            <dt>Sharing:</dt>
            <dd data-testid="vm-visibility">
              <span v-if="isPublic" class="vm-badge-public">PUBLIC</span>
              <template v-else>Private</template>
              <template v-if="users.length">
                · {{ users.length }} user{{ users.length === 1 ? "" : "s" }}</template
              >
              <template v-if="links.length">
                · {{ links.length }} link{{ links.length === 1 ? "" : "s" }}</template
              >
            </dd>
            <span>
              <button
                type="button"
                class="vm-detail-btn"
                data-testid="vm-share"
                @click="sharing = true"
              >
                <i class="pi pi-share-alt" aria-hidden="true" /> Share
              </button>
            </span>

            <dt>Network access:</dt>
            <dd>{{ integrationsOnly ? "Integrations only" : "All destinations" }}</dd>
            <span v-tooltip.top="why('egress')">
              <button
                type="button"
                class="vm-detail-btn"
                :disabled="!!why('egress')"
                @click="changeEgress"
              >
                Change
              </button>
            </span>

            <dt>Email:</dt>
            <dd class="vm-email">
              <span>{{ vm.email_receive_enabled ? "Send & receive" : "Send only" }}</span>
              <span v-if="vm.email_receive_enabled && vm.email_address">{{
                vm.email_address
              }}</span>
            </dd>
            <span v-tooltip.top="why('email')">
              <button
                type="button"
                class="vm-detail-btn"
                :disabled="!!why('email')"
                @click="toggleEmail"
              >
                Configure
              </button>
            </span>
          </dl>
        </div>

        <p v-if="reasons" class="vm-denied" data-testid="vm-denied">{{ reasons }}</p>
      </div>
    </div>

    <Modal :is-open="sharing" :title="`Share ${name}`" @close="sharing = false">
      <div v-if="vm" class="vm-share" data-testid="vm-share-dialog">
        <div class="vm-share-row">
          <span>
            <strong>{{ isPublic ? "Public" : "Private" }}</strong>
            <span class="project-muted">
              {{
                isPublic
                  ? "Anyone on the internet can reach it."
                  : "Only people it's shared with, logged in to exe.dev."
              }}</span
            >
          </span>
          <span v-tooltip.top="why(isPublic ? 'private' : 'public')">
            <Button
              :label="isPublic ? 'Make private' : 'Make public'"
              size="small"
              outlined
              :severity="isPublic ? undefined : 'danger'"
              :disabled="!!why(isPublic ? 'private' : 'public')"
              data-testid="vm-visibility-toggle"
              @click="toggleVisibility"
            />
          </span>
        </div>
        <h4>People</h4>
        <ul class="vm-share-list" data-testid="vm-shares">
          <li v-if="vm.sharing.team_shared">Your team</li>
          <li v-for="user in users" :key="user.email">
            <span>
              {{ user.email }}
              <span class="project-muted"
                >{{ user.share_level === "root" ? "shell" : "web"
                }}{{ user.status && user.status !== "active" ? `, ${user.status}` : "" }}</span
              >
            </span>
            <span v-tooltip.top="why('share-remove')">
              <Button
                label="Remove"
                text
                size="small"
                :disabled="!!why('share-remove')"
                @click="shareRemove(user.email)"
              />
            </span>
          </li>
          <li v-if="!users.length && !vm.sharing.team_shared" class="project-muted">
            Not shared with anyone.
          </li>
        </ul>
        <span v-tooltip.top="why('share-add')">
          <Button
            label="Share with someone…"
            icon="pi pi-user-plus"
            text
            size="small"
            :disabled="!!why('share-add')"
            @click="shareAdd"
          />
        </span>
        <h4>Links</h4>
        <ul class="vm-share-list">
          <li v-for="link in links" :key="link.token">
            <a :href="link.url" target="_blank" rel="noopener">…{{ link.token.slice(-6) }}</a>
            <span v-tooltip.top="why('link-remove')">
              <Button
                label="Revoke"
                text
                size="small"
                :disabled="!!why('link-remove')"
                @click="linkRemove(link.token)"
              />
            </span>
          </li>
          <li v-if="!links.length" class="project-muted">No share links.</li>
        </ul>
        <span v-tooltip.top="why('link-add')">
          <Button
            label="Create link…"
            icon="pi pi-link"
            text
            size="small"
            :disabled="!!why('link-add')"
            @click="linkAdd"
          />
        </span>
      </div>
    </Modal>

    <Modal :is-open="!!pending" :title="pending?.title ?? ''" @close="close">
      <form v-if="pending" id="vm-change-form" class="project-form" @submit.prevent="confirm">
        <p :class="{ 'project-notice': pending.warning }">{{ pending.description }}</p>
        <template v-if="pending.input">
          <label for="vm-change-value">{{ pending.input.label }}</label>
          <input
            id="vm-change-value"
            v-model="draft"
            :type="pending.input.type ?? 'text'"
            :placeholder="pending.input.placeholder"
            :maxlength="pending.input.maxlength"
            :required="!pending.input.optional"
            autocomplete="off"
            data-testid="vm-change-value"
          />
        </template>
        <template v-if="pending.change.kind === 'resize'">
          <div class="vm-resize">
            <label>vCPUs <input v-model.number="sizeDraft.cpus" type="number" min="1" /></label>
            <label
              >Memory (GB) <input v-model.number="sizeDraft.memory_gb" type="number" min="1"
            /></label>
            <label
              >Disk (GB) <input v-model.number="sizeDraft.disk_gb" type="number" :min="diskGB"
            /></label>
          </div>
        </template>
        <template v-if="pending.typed">
          <label for="vm-change-typed"
            >Type <strong>{{ pending.typed }}</strong> to confirm</label
          >
          <input
            id="vm-change-typed"
            v-model="typed"
            autocomplete="off"
            data-testid="vm-change-typed"
          />
        </template>
        <p v-if="changeError" class="error-line" role="alert">{{ changeError }}</p>
      </form>
      <template #footer>
        <Button label="Cancel" text severity="secondary" :disabled="busy" @click="close" />
        <Button
          type="submit"
          form="vm-change-form"
          :label="pending?.confirm"
          :severity="pending?.danger ? 'danger' : undefined"
          :loading="busy"
          :disabled="!ready"
          data-testid="vm-change-confirm"
        />
      </template>
    </Modal>
  </section>
</template>

<script setup lang="ts">
import { computed, ref, watch } from "vue";
import Button from "primevue/button";
import Modal from "./Modal.vue";
import {
  exeVmsApi,
  type ExeVMChange,
  type ExeVMChangeKind,
  type ExeVMSettings,
} from "../../services/exeVms";
import { errorMessage } from "../../utils/errorMessage";

const props = defineProps<{ name: string }>();
const emit = defineEmits<{ renamed: [name: string]; deleted: []; open: [name: string] }>();

const SHARE_KINDS: ExeVMChangeKind[] = ["share-add", "share-remove", "link-add", "link-remove"];
const DANGER_KINDS: ExeVMChangeKind[] = ["rename", "copy", "restart", "pause", "resume", "delete"];
const ALL_KINDS: ExeVMChangeKind[] = [
  "comment",
  "tag-add",
  "tag-remove",
  "port",
  "public",
  "private",
  ...SHARE_KINDS,
  "domain-add",
  "domain-remove",
  "egress",
  "email",
  "resize",
  ...DANGER_KINDS,
];

const vm = ref<ExeVMSettings | null>(null);
const loadError = ref("");

async function load() {
  const name = props.name;
  loadError.value = "";
  try {
    const settings = await exeVmsApi.settings(name);
    if (name === props.name) vm.value = settings;
  } catch (err) {
    if (name === props.name) loadError.value = errorMessage(err);
  }
}
watch(
  () => props.name,
  () => {
    vm.value = null;
    void load();
  },
  { immediate: true },
);

const GB = 1024 ** 3;
const diskGB = computed(() => Math.round((vm.value?.disk_capacity_bytes ?? 0) / GB));
const size = computed(() => {
  const v = vm.value;
  if (!v) return "";
  const parts = [];
  if (v.allocated_cpus) parts.push(`${v.allocated_cpus} vCPU`);
  if (v.memory_capacity_bytes) parts.push(`${Math.round(v.memory_capacity_bytes / GB)} GB`);
  if (v.disk_capacity_bytes) parts.push(`${diskGB.value} GB Disk`);
  return parts.join(" · ") || "Unknown";
});
const isPublic = computed(() => vm.value?.proxy_share === "public");
const integrationsOnly = computed(() => vm.value?.egress_policy === "integrations");
const users = computed(() => vm.value?.sharing.named_user_shares ?? []);
const links = computed(() => vm.value?.share_links ?? []);

function why(kind: ExeVMChangeKind): string {
  return vm.value?.denied[kind] ?? "";
}
/** Why the changes that can't be made here can't, each reason once. */
const reasons = computed(() => [...new Set(ALL_KINDS.map(why).filter(Boolean))].join(" "));
const created = computed(() => {
  const at = vm.value?.created_at;
  if (!at) return "";
  const date = new Date(at);
  const day = date.toLocaleDateString("en-US", {
    month: "short",
    day: "numeric",
    year: "numeric",
    timeZone: "UTC",
  });
  return `${day} ${date.toISOString().slice(11, 16)} UTC`;
});
const sharing = ref(false);
const headerActions = computed(() => [
  { kind: "copy" as const, label: "Copy", icon: "pi-copy", run: copy },
  { kind: "restart" as const, label: "Restart", icon: "pi-refresh", run: restart },
  vm.value?.status === "paused"
    ? { kind: "resume" as const, label: "Resume", icon: "pi-play", run: resume }
    : { kind: "pause" as const, label: "Pause", icon: "pi-pause", run: pause },
  { kind: "delete" as const, label: "Delete", icon: "pi-trash", run: remove },
]);

const copied = ref(false);
// Clicking the box copies, unless the click ends a selection of its text.
function copyUnlessSelecting() {
  if (window.getSelection()?.toString()) return;
  void copySsh();
}
async function copySsh() {
  await navigator.clipboard.writeText(vm.value?.ssh_command ?? "");
  copied.value = true;
  setTimeout(() => (copied.value = false), 2000);
}

// The change being confirmed.
interface Pending {
  title: string;
  description: string;
  change: ExeVMChange;
  confirm: string;
  danger?: boolean;
  /** Shows the description as a warning. */
  warning?: boolean;
  /** An input, whose text becomes the change's value. */
  input?: {
    label: string;
    placeholder?: string;
    type?: "text" | "number" | "email";
    maxlength?: number;
    optional?: boolean;
  };
  /** What the user types to confirm. */
  typed?: string;
}
const pending = ref<Pending | null>(null);
const draft = ref("");
const typed = ref("");
const sizeDraft = ref({ cpus: 0, memory_gb: 0, disk_gb: 0 });
const busy = ref(false);
const changeError = ref("");
const ready = computed(
  () =>
    !!pending.value &&
    !busy.value &&
    (!pending.value.typed || typed.value === pending.value.typed) &&
    (!pending.value.input || pending.value.input.optional || draft.value.trim() !== "") &&
    (pending.value.change.kind !== "resize" || Object.values(resizeChange()).some(Boolean)),
);

function ask(next: Pending, value = "") {
  pending.value = next;
  draft.value = value;
  typed.value = "";
  changeError.value = "";
}
function close() {
  if (!busy.value) pending.value = null;
}

async function confirm() {
  const p = pending.value;
  if (!p || !ready.value) return;
  const change: ExeVMChange = { ...p.change };
  if (p.input) change.value = draft.value.trim();
  if (change.kind === "resize") Object.assign(change, resizeChange());
  busy.value = true;
  changeError.value = "";
  try {
    await exeVmsApi.change(props.name, change);
    busy.value = false;
    pending.value = null;
    if (change.kind === "rename") emit("renamed", change.value ?? "");
    else if (change.kind === "copy") emit("open", change.value ?? "");
    else if (change.kind === "delete") emit("deleted");
    else await load();
  } catch (err) {
    changeError.value = errorMessage(err);
  } finally {
    busy.value = false;
  }
}

const name = computed(() => props.name);
function editComment() {
  ask(
    {
      title: vm.value?.comment ? "Edit comment" : "Add comment",
      description:
        "A short plain-text note shown next to the VM on exe.dev. Leave it empty to clear it.",
      change: { kind: "comment" },
      confirm: "Save comment",
      input: { label: "Comment", placeholder: "e.g. staging copy", maxlength: 200, optional: true },
    },
    vm.value?.comment ?? "",
  );
}
function addTag() {
  ask({
    title: "Add tag",
    description: `Tags group VMs on exe.dev, and integrations attached to a tag reach every VM with it, ${name.value} included.`,
    change: { kind: "tag-add" },
    confirm: "Add tag",
    input: { label: "Tag", placeholder: "e.g. prod" },
  });
}
function removeTag(tag: string) {
  ask({
    title: "Remove tag",
    description: `Remove #${tag} from ${name.value}. Integrations attached to #${tag} no longer reach it.`,
    change: { kind: "tag-remove", value: tag },
    confirm: "Remove tag",
    danger: true,
  });
}
function changePort() {
  ask(
    {
      title: "Set proxy port",
      description: `The port on the VM that ${vm.value?.https_url} connects to.`,
      change: { kind: "port" },
      confirm: "Set port",
      input: { label: "Port", type: "number", placeholder: "8000" },
    },
    String(vm.value?.proxy_port ?? ""),
  );
}
function toggleVisibility() {
  if (isPublic.value)
    ask({
      title: "Make private",
      description: `Only people logged in to exe.dev with access to ${name.value} can reach ${vm.value?.https_url}.`,
      change: { kind: "private" },
      confirm: "Make private",
    });
  else
    ask({
      title: "Make public",
      description: `Anyone on the internet can reach ${vm.value?.https_url}, without logging in. Whatever the VM serves on port ${vm.value?.proxy_port} becomes public.`,
      change: { kind: "public" },
      confirm: "Make public",
      danger: true,
      warning: true,
    });
}
function shareAdd() {
  ask({
    title: "Share VM",
    description: `exe.dev emails an invitation, and the person can then reach ${vm.value?.https_url} (web access, not a shell).`,
    change: { kind: "share-add" },
    confirm: "Share",
    input: { label: "Email", type: "email", placeholder: "user@example.com" },
  });
}
function shareRemove(email: string) {
  ask({
    title: "Stop sharing",
    description: `${email} can no longer reach ${name.value}.`,
    change: { kind: "share-remove", value: email },
    confirm: "Stop sharing",
    danger: true,
  });
}
function linkAdd() {
  ask({
    title: "Create share link",
    description: `Anyone with the link can reach ${vm.value?.https_url} until it is revoked.`,
    change: { kind: "link-add" },
    confirm: "Create link",
  });
}
function linkRemove(token: string) {
  ask({
    title: "Revoke share link",
    description: `The link …${token.slice(-6)} stops working.`,
    change: { kind: "link-remove", value: token },
    confirm: "Revoke link",
    danger: true,
  });
}
function domainAdd() {
  let host = name.value;
  try {
    host = new URL(vm.value?.https_url ?? "").hostname;
  } catch {
    // The VM's name is the next best CNAME target.
  }
  ask({
    title: "Add custom domain",
    description: `First point the domain's CNAME record at ${host}. Requests to the domain are then routed to ${name.value}.`,
    change: { kind: "domain-add" },
    confirm: "Add domain",
    input: { label: "Domain", placeholder: "app.example.com" },
  });
}
function domainRemove(domain: string) {
  ask({
    title: "Remove custom domain",
    description: `Requests to ${domain} are no longer routed to ${name.value}.`,
    change: { kind: "domain-remove", value: domain },
    confirm: "Remove domain",
    danger: true,
  });
}
function changeEgress() {
  ask(
    integrationsOnly.value
      ? {
          title: "Allow all destinations",
          description: `${name.value} may connect anywhere on the internet.`,
          change: { kind: "egress", value: "all" },
          confirm: "Allow all",
        }
      : {
          title: "Integrations only",
          description: `${name.value} may connect only to exe.dev's metadata service and its attached integrations; other outbound connections are blocked.`,
          change: { kind: "egress", value: "integrations" },
          confirm: "Restrict",
        },
  );
}
function toggleEmail() {
  ask(
    vm.value?.email_receive_enabled
      ? {
          title: "Stop receiving email",
          description: `${name.value} stops accepting incoming email.`,
          change: { kind: "email", value: "off" },
          confirm: "Stop receiving",
        }
      : {
          title: "Receive email",
          description: `${name.value} accepts email to any address at its domain, delivered to ~/Maildir/new/.`,
          change: { kind: "email", value: "on" },
          confirm: "Receive email",
        },
  );
}
function openResize() {
  const v = vm.value;
  sizeDraft.value = {
    cpus: v?.allocated_cpus ?? 0,
    memory_gb: Math.round((v?.memory_capacity_bytes ?? 0) / GB),
    disk_gb: diskGB.value,
  };
  ask({
    title: `Resize ${name.value}`,
    description: `Now ${size.value}. CPU and memory changes take effect after a restart; the disk can only grow, and growing it needs a reboot.`,
    change: { kind: "resize" },
    confirm: "Resize",
    danger: true,
  });
}
// The sizes that changed (0 keeps one).
function resizeChange() {
  const v = vm.value;
  const now = {
    cpus: v?.allocated_cpus ?? 0,
    memory_gb: Math.round((v?.memory_capacity_bytes ?? 0) / GB),
    disk_gb: diskGB.value,
  };
  const next = sizeDraft.value;
  return {
    cpus: next.cpus !== now.cpus ? next.cpus : 0,
    memory_gb: next.memory_gb !== now.memory_gb ? next.memory_gb : 0,
    disk_gb: next.disk_gb !== now.disk_gb ? next.disk_gb : 0,
  };
}
function rename() {
  ask(
    {
      title: "Rename VM",
      description: `Its address changes too: ${vm.value?.https_url} and its SSH host stop working.`,
      change: { kind: "rename" },
      confirm: "Rename",
      danger: true,
      input: { label: "New name", placeholder: "new-name" },
    },
    name.value,
  );
}
function copy() {
  ask(
    {
      title: `Copy ${name.value}`,
      description: `Create a new VM from a copy of ${name.value}'s disk, with its size and tags. It counts against your plan like any other VM.`,
      change: { kind: "copy" },
      confirm: "Copy VM",
      input: { label: "New VM name", placeholder: `${name.value}-copy` },
    },
    `${name.value}-copy`,
  );
}
function pause() {
  ask({
    title: "Pause VM",
    description: `Snapshot ${name.value}'s memory to disk and free its CPU and memory. Its web page and SSH stop answering until it is resumed.`,
    change: { kind: "pause" },
    confirm: "Pause",
    danger: true,
  });
}
function resume() {
  ask({
    title: "Resume VM",
    description: `Restore ${name.value}'s memory from disk and carry on where it was paused.`,
    change: { kind: "resume" },
    confirm: "Resume",
  });
}
function restart() {
  ask({
    title: "Restart VM",
    description: `Restart ${name.value}: everything running on it stops and starts again.`,
    change: { kind: "restart" },
    confirm: "Restart",
    danger: true,
  });
}
function remove() {
  ask({
    title: "Delete VM",
    description: `Permanently delete ${name.value} and all its data. This cannot be undone.`,
    change: { kind: "delete" },
    confirm: "Delete VM",
    danger: true,
    warning: true,
    typed: name.value,
  });
}
</script>

<style scoped>
.vm-card {
  display: flex;
  flex-direction: column;
  gap: 1.5rem;
  max-width: 56rem;
  min-width: 0;
  padding: 1.25rem 1.5rem;
  border: 1px solid var(--border);
  border-radius: 0.5rem;
  background: var(--bg-base);
  font-family: var(--font-mono);
  font-size: var(--font-size-13);
  container-type: inline-size;
}
.vm-header {
  display: flex;
  flex-wrap: wrap;
  align-items: center;
  gap: 0.625rem;
}
.vm-emoji {
  font-size: 1.25rem;
}
.vm-name {
  margin: 0;
  font-size: 1.25rem;
  font-weight: 600;
  overflow-wrap: anywhere;
}
.vm-title-btn,
.vm-header-btn {
  display: inline-flex;
  align-items: center;
  gap: 0.25rem;
  min-height: 1.75rem;
  padding: 0.25rem 0.375rem;
  border: 0;
  border-radius: 0.25rem;
  background: transparent;
  color: var(--text-secondary);
  font: inherit;
  cursor: pointer;
}
.vm-title-btn:hover:not(:disabled),
.vm-header-btn:hover:not(:disabled) {
  background: var(--bg-hover);
  color: var(--text-primary);
}
.vm-header-btn.danger {
  color: var(--error-text);
}
.vm-header-btn.danger:hover:not(:disabled) {
  background: var(--error-bg);
  color: var(--error-text);
}
.vm-header-actions {
  display: inline-flex;
  flex-wrap: wrap;
  gap: 0.375rem;
  margin-left: auto;
}
.vm-badge-public {
  padding: 0.0625rem 0.375rem;
  border-radius: 0.1875rem;
  background: var(--warning-bg);
  color: var(--warning-text);
  font-size: var(--font-size-11);
  font-weight: 600;
}
.vm-links {
  display: flex;
  flex-wrap: wrap;
  align-items: center;
  gap: 0.75rem 1rem;
}
.vm-ssh {
  display: inline-flex;
  align-items: center;
  min-width: 0;
  max-width: 100%;
  padding: 0.1875rem 0.25rem 0.1875rem 0.625rem;
  border: 1px solid var(--border);
  border-radius: 0.375rem;
  background: var(--bg-secondary);
  cursor: pointer;
}
.vm-ssh.copied {
  border-color: var(--green-600);
}
.vm-ssh code {
  overflow-x: auto;
  white-space: nowrap;
  background: none;
  font-family: inherit;
  user-select: text;
}
.vm-ssh-prompt {
  margin-right: 0.375rem;
  color: var(--text-secondary);
  user-select: none;
}
.vm-ssh-btn {
  padding: 0.25rem 0.375rem;
  border: 0;
  background: none;
  color: var(--text-secondary);
  cursor: pointer;
}
.vm-link-btns {
  display: flex;
  flex-wrap: wrap;
  gap: 0.375rem;
  margin-left: auto;
}
.vm-link,
.vm-action-btn,
.vm-detail-btn {
  display: inline-flex;
  align-items: center;
  gap: 0.3rem;
  border: 1px solid var(--border);
  background: var(--bg-base);
  color: var(--text-primary);
  font: inherit;
  text-decoration: none;
  cursor: pointer;
}
.vm-link {
  padding: 0.3rem 0.75rem;
  border-radius: 0.375rem;
}
.vm-action-btn {
  padding: 0.3rem 0.625rem;
  border-radius: 0.25rem;
}
.vm-detail-btn {
  padding: 0.125rem 0.5rem;
  border-radius: 0.25rem;
  font-size: var(--font-size-12);
}
.vm-link:hover,
.vm-action-btn:hover:not(:disabled),
.vm-detail-btn:hover:not(:disabled) {
  background: var(--bg-hover);
}
button:disabled {
  opacity: 0.45;
  cursor: default;
}
.vm-grid {
  display: grid;
  grid-template-columns: 1fr 1fr;
  gap: 0.5rem 1rem;
  align-items: start;
}
.vm-col {
  display: grid;
  grid-template-columns: max-content minmax(0, 1fr);
  gap: 0.5rem 0.75rem;
  align-items: center;
  margin: 0;
}
.vm-access {
  grid-template-columns: max-content minmax(0, max-content) max-content;
}
.vm-col dt {
  color: var(--text-secondary);
}
.vm-col dd {
  display: flex;
  flex-wrap: wrap;
  align-items: center;
  gap: 0.25rem 0.5rem;
  min-width: 0;
  margin: 0;
  overflow-wrap: anywhere;
}
.vm-comment {
  font-style: italic;
}
.vm-empty {
  color: var(--text-secondary);
}
.vm-chips {
  gap: 0.25rem;
}
.vm-tag,
.vm-domain {
  display: inline-flex;
  align-items: center;
  gap: 0.125rem;
  padding: 0.0625rem 0.375rem;
  border-radius: 0.1875rem;
  background: var(--bg-tertiary);
  color: var(--text-secondary);
}
.vm-domain a {
  color: var(--text-primary);
  text-decoration: none;
}
.vm-chip-remove {
  padding: 0 0.125rem;
  border: none;
  background: none;
  color: var(--text-secondary);
  font-size: var(--font-size-14);
  line-height: 1;
  cursor: pointer;
}
.vm-chip-remove:hover {
  color: var(--error-text);
}
.vm-col .vm-email {
  flex-direction: column;
  align-items: flex-start;
}
.vm-denied {
  margin: 0;
  color: var(--text-secondary);
  font-family: var(--font-sans, inherit);
  font-size: var(--font-size-12);
}
.vm-share {
  display: flex;
  flex-direction: column;
  gap: 0.5rem;
}
.vm-share h4 {
  margin: 0.5rem 0 0;
}
.vm-share-row,
.vm-share-list li {
  display: flex;
  align-items: center;
  justify-content: space-between;
  gap: 0.75rem;
}
.vm-share-row > span:first-child {
  display: flex;
  flex-direction: column;
  min-width: 0;
}
.vm-share-row > span:last-child {
  flex-shrink: 0;
}
.vm-share-list {
  margin: 0;
  padding: 0;
  list-style: none;
}
.vm-share-list a {
  color: var(--link-color);
}
.vm-resize {
  display: flex;
  gap: 0.75rem;
}
.vm-resize label {
  display: flex;
  flex-direction: column;
  gap: 0.25rem;
  font-size: var(--font-size-13);
}
@container (max-width: 44rem) {
  .vm-grid {
    grid-template-columns: 1fr;
  }
}
</style>
