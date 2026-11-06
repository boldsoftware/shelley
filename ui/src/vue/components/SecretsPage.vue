<!-- The secrets management page, /secrets (docs/secrets.md): every stored
     secret with its name (renameable), kind, source conversation and where
     write_secret put it; reveal, copy and delete after a confirmation.
     /secrets#ID highlights one. -->
<template>
  <div class="secrets-page">
    <header class="secrets-header">
      <a href="/" class="secrets-back">← Shelley</a>
      <h1>Secrets</h1>
    </header>
    <p class="secrets-intro text-secondary">
      Secrets captured from the composer. The model sees only <code v-pre>{{secret:ID}}</code
      >; its <code>write_secret</code> tool writes a value to a file (mode 0600) without showing it,
      and any copy of a value in tool output is replaced with its placeholder.
    </p>
    <div v-if="error" class="secrets-error" role="alert">{{ error }}</div>
    <div v-if="secrets === null" class="secrets-empty text-secondary">Loading…</div>
    <div v-else-if="secrets.length === 0" class="secrets-empty text-secondary">No secrets yet.</div>
    <article
      v-for="s in secrets"
      :id="s.id"
      :key="s.id"
      :class="['secret-card', { 'secret-card-target': s.id === target }]"
      data-testid="secret-card"
    >
      <div class="secret-card-head">
        <i class="pi pi-lock" aria-hidden="true" />
        <input
          class="secret-name"
          :value="s.name"
          :aria-label="`Name of ${s.id}`"
          @change="rename(s, ($event.target as HTMLInputElement).value)"
        />
        <code class="secret-id text-secondary">{{ s.id }}</code>
      </div>
      <dl class="secret-facts">
        <dt class="text-secondary">Kind</dt>
        <dd>{{ secretKindLabel(s.kind) }}</dd>
        <dt class="text-secondary">Created</dt>
        <dd>{{ when(s.created_at) }}</dd>
        <dt class="text-secondary">From</dt>
        <dd>
          <a v-if="s.conversation_id" :href="`/c/${s.conversation_slug ?? s.conversation_id}`">{{
            s.conversation_slug ?? s.conversation_id
          }}</a>
          <template v-else>No conversation</template>
        </dd>
        <dt class="text-secondary">Written to</dt>
        <dd>
          <ul v-if="s.injections.length" class="secret-injections">
            <li v-for="(inj, i) in s.injections" :key="i">
              <code>{{ inj.machine }}:{{ inj.path }}</code>
              <span class="secret-when text-secondary">{{ when(inj.created_at) }}</span>
            </li>
          </ul>
          <template v-else>Nowhere yet</template>
        </dd>
      </dl>
      <div v-if="revealed[s.id] !== undefined" class="secret-value">
        <code>{{ revealed[s.id] }}</code>
      </div>
      <div class="secret-actions">
        <template v-if="confirming?.id === s.id">
          <span class="secret-confirm-text">{{ confirmText(confirming.action) }}</span>
          <button type="button" class="secret-btn text-error" @click="confirm(s)">
            {{
              confirming.action === "delete"
                ? "Delete"
                : confirming.action === "copy"
                  ? "Copy"
                  : "Reveal"
            }}
          </button>
          <button type="button" class="secret-btn" @click="confirming = null">Cancel</button>
        </template>
        <template v-else>
          <button
            v-if="revealed[s.id] !== undefined"
            type="button"
            class="secret-btn"
            @click="hide(s.id)"
          >
            Hide
          </button>
          <button
            v-else
            type="button"
            class="secret-btn"
            @click="confirming = { id: s.id, action: 'reveal' }"
          >
            Reveal
          </button>
          <button
            type="button"
            class="secret-btn"
            @click="confirming = { id: s.id, action: 'copy' }"
          >
            {{ copied === s.id ? "Copied" : "Copy" }}
          </button>
          <button
            type="button"
            class="secret-btn"
            disabled
            title="Coming later: move this secret into an exe.dev integration and delete it here"
          >
            Move to integration
          </button>
          <button
            type="button"
            class="secret-btn text-error"
            @click="confirming = { id: s.id, action: 'delete' }"
          >
            Delete
          </button>
        </template>
      </div>
    </article>
  </div>
</template>

<script setup lang="ts">
import { onMounted, reactive, ref } from "vue";
import { secretsApi, type Secret } from "../../services/api";
import { secretKindLabel } from "../../utils/secretDetect";

type Action = "reveal" | "copy" | "delete";

const secrets = ref<Secret[] | null>(null);
const error = ref("");
const target = ref(decodeURIComponent(window.location.hash.slice(1)));
const revealed = reactive<Record<string, string>>({});
const confirming = ref<{ id: string; action: Action } | null>(null);
const copied = ref("");

function when(iso: string): string {
  return new Date(iso).toLocaleString();
}

function confirmText(action: Action): string {
  if (action === "delete")
    return "Delete this secret? Messages keep its placeholder; write_secret can no longer use it.";
  return `${action === "copy" ? "Copy" : "Show"} the value? It will be visible on this screen or in your clipboard.`;
}

async function run(f: () => Promise<void>) {
  error.value = "";
  try {
    await f();
  } catch (e) {
    error.value = e instanceof Error ? e.message : String(e);
  }
}

async function load() {
  await run(async () => {
    secrets.value = await secretsApi.list();
  });
}

async function rename(s: Secret, name: string) {
  await run(async () => {
    await secretsApi.rename(s.id, name.trim());
    s.name = name.trim();
  });
}

function hide(id: string) {
  delete revealed[id];
}

async function confirm(s: Secret) {
  const action = confirming.value?.action;
  confirming.value = null;
  await run(async () => {
    if (action === "delete") {
      await secretsApi.remove(s.id);
      hide(s.id);
      await load();
    } else if (action === "reveal") {
      revealed[s.id] = await secretsApi.reveal(s.id);
    } else if (action === "copy") {
      await navigator.clipboard.writeText(await secretsApi.reveal(s.id));
      copied.value = s.id;
    }
  });
}

onMounted(async () => {
  await load();
  if (target.value) document.getElementById(target.value)?.scrollIntoView({ block: "center" });
});
window.addEventListener("hashchange", () => {
  target.value = decodeURIComponent(window.location.hash.slice(1));
});
</script>
