<template>
  <div class="drawer-bulk-actions">
    <input
      type="checkbox"
      :checked="allSelected"
      :indeterminate="selected.length > 0 && !allSelected"
      :disabled="busy"
      :aria-label="`Select all ${all.length} matching conversations`"
      @change="$emit('toggle-all')"
    />
    <span class="text-sm drawer-bulk-count">{{ selected.length }} selected</span>
    <Button
      icon="pi pi-tags"
      class="btn-icon"
      text
      severity="secondary"
      aria-label="Tag selected conversations"
      v-tooltip.top="'Tags'"
      :disabled="!selected.length || busy"
      @click="openTags"
    />
    <Button
      icon="pi pi-folder"
      class="btn-icon"
      text
      severity="secondary"
      aria-label="Move to project"
      v-tooltip.top="canMove ? 'Move to project' : 'Select your own idle conversations to move'"
      :disabled="!canMove || busy"
      @click="projectPopover?.toggle($event)"
    />
    <Button
      :icon="archivedOnly ? 'pi pi-undo' : 'pi pi-inbox'"
      class="btn-icon"
      text
      severity="secondary"
      :aria-label="
        archivedOnly ? 'Restore selected conversations' : 'Archive selected conversations'
      "
      v-tooltip.top="archivedOnly ? 'Restore' : 'Archive'"
      :disabled="!selected.length || busy"
      @click="$emit('archive')"
    />
    <Button
      icon="pi pi-times"
      class="btn-icon"
      text
      severity="secondary"
      aria-label="Cancel selection"
      v-tooltip.top="'Cancel selection'"
      :disabled="busy"
      @click="$emit('cancel')"
    />
  </div>
  <Popover ref="tagPopover" :pt="{ root: { class: 'drawer-bulk-popover' } }">
    <form @submit.prevent="addTag">
      <div class="text-sm">Tags on {{ selected.length }} conversations</div>
      <div class="text-secondary text-xs">A dash means some. Untouched tags stay unchanged.</div>
      <div class="tag-filter-options">
        <label v-for="tag in tagOptions" :key="tag" class="tag-filter-option">
          <input
            type="checkbox"
            :checked="tagChecked(tag)"
            :indeterminate="tagMixed(tag)"
            :disabled="busy"
            @change="changes[tag] = ($event.target as HTMLInputElement).checked"
          />
          <span class="tag-filter-option-name">#{{ tag }}</span>
          <span class="text-secondary">{{ tagCount(tag) }}/{{ selected.length }}</span>
        </label>
      </div>
      <div class="conversation-tag-inline-form drawer-bulk-new-tag">
        <span class="conversation-tag-hash">#</span>
        <input
          v-model="newTag"
          class="conversation-tag-inline-input"
          placeholder="New tag…"
          aria-label="New tag"
          :disabled="busy"
        />
        <Button
          label="Add"
          type="submit"
          text
          size="small"
          severity="secondary"
          :disabled="busy || !newTag.trim()"
        />
      </div>
      <div class="drawer-bulk-popover-actions">
        <Button
          label="Cancel"
          text
          size="small"
          severity="secondary"
          :disabled="busy"
          @click="tagPopover?.hide()"
        />
        <Button
          label="Apply tags"
          text
          size="small"
          severity="secondary"
          :disabled="busy"
          @click="applyTags"
        />
      </div>
    </form>
  </Popover>
  <Popover ref="projectPopover" :pt="{ root: { class: 'drawer-bulk-popover' } }">
    <div class="text-sm">Move {{ selected.length }} conversations</div>
    <div class="tag-filter-options">
      <button
        v-for="project in projects"
        :key="project.id"
        type="button"
        class="tag-filter-option"
        :disabled="busy"
        @click="move(project.name)"
      >
        <i class="pi pi-folder" aria-hidden="true" />
        <span class="tag-filter-option-name">{{ project.name }}</span>
      </button>
    </div>
    <Button label="Cancel" text size="small" severity="secondary" @click="projectPopover?.hide()" />
  </Popover>
</template>

<script setup lang="ts">
import { computed, ref } from "vue";
import Button from "primevue/button";
import Popover from "primevue/popover";
import type { Conversation } from "../../types";
import type { Project } from "../../services/workspace";
import { parseTags } from "./conversationDrawerShared";

const props = defineProps<{
  selected: Conversation[];
  all: Conversation[];
  projects: Project[];
  allSelected: boolean;
  archivedOnly: boolean;
  canMove: boolean;
  busy: boolean;
  applyTags: (changes: Record<string, boolean>) => Promise<boolean>;
  move: (project: string) => Promise<boolean>;
}>();
defineEmits<{
  (e: "toggle-all"): void;
  (e: "cancel"): void;
  (e: "archive"): void;
}>();
const tagPopover = ref<InstanceType<typeof Popover> | null>(null);
const projectPopover = ref<InstanceType<typeof Popover> | null>(null);
const changes = ref<Record<string, boolean>>({});
const newTag = ref("");
const tagOptions = computed(() =>
  [...new Set([...props.all.flatMap(parseTags), ...Object.keys(changes.value)])].sort(),
);
const tagCount = (tag: string) => props.selected.filter((c) => parseTags(c).includes(tag)).length;
const tagChecked = (tag: string) => changes.value[tag] ?? tagCount(tag) === props.selected.length;
const tagMixed = (tag: string) =>
  !(tag in changes.value) && tagCount(tag) > 0 && tagCount(tag) < props.selected.length;

function openTags(event: Event) {
  changes.value = {};
  newTag.value = "";
  tagPopover.value?.toggle(event);
}
function addTag() {
  const tag = newTag.value.trim().replace(/^#+/, "");
  if (tag) changes.value[tag] = true;
  newTag.value = "";
}
async function applyTags() {
  addTag();
  if (await props.applyTags({ ...changes.value })) tagPopover.value?.hide();
}
async function move(project: string) {
  if (await props.move(project)) projectPopover.value?.hide();
}
</script>
