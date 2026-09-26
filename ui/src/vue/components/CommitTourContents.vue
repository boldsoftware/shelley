<!-- Narrative-order file groups. Each filename is shown once above its change links. -->
<template>
  <nav aria-label="Tour contents">
    <ol class="diff-viewer-tour-contents">
      <li v-if="layout.overview" class="diff-viewer-tour-contents-item overview">
        <button
          type="button"
          :data-tour-target="layout.overview.anchor"
          :title="layout.overview.label"
          :aria-current="layout.overview.anchor === activeAnchor ? 'location' : undefined"
          @click="emit('select', layout.overview.anchor)"
        >
          {{ layout.overview.label }}
        </button>
      </li>
      <template v-for="group in layout.groups" :key="groupKey(group)">
        <li v-if="group.section" class="diff-viewer-tour-contents-item section">
          <button
            type="button"
            :data-tour-target="group.section.anchor"
            :title="group.section.label"
            :aria-current="group.section.anchor === activeAnchor ? 'location' : undefined"
            @click="emit('select', group.section.anchor)"
          >
            {{ group.section.label }}
          </button>
        </li>
        <li v-if="group.rows.length > 0" class="diff-viewer-tour-contents-tree">
          <div
            class="tour-file-tree"
            role="list"
            :aria-label="group.section ? `Changes in ${group.section.label}` : 'Tour changes'"
          >
            <div
              v-for="row in group.rows"
              :key="row.key"
              :class="row.kind === 'directory' ? 'tour-file-tree-directory' : 'tour-file-tree-item'"
              role="listitem"
            >
              <div v-if="row.kind === 'directory'" class="diff-tree-row" :title="row.label">
                <span class="diff-tree-icon" aria-hidden="true">
                  <svg width="12" height="12" viewBox="0 0 16 16">
                    <path
                      fill="none"
                      stroke="currentColor"
                      stroke-width="1.2"
                      d="M1.5 3.5h5l1.5 2h6.5v7h-13v-9z"
                    />
                  </svg>
                </span>
                <span class="diff-tree-label ellipsis-start">{{ row.label }}</span>
              </div>
              <button
                v-else-if="row.kind === 'file'"
                type="button"
                class="diff-tree-row tour-file-heading"
                :style="{ paddingLeft: `calc(0.375rem + ${row.depth} * 0.85rem)` }"
                :title="row.label"
                :aria-label="`Go to first change in ${row.label}`"
                @click="emit('select', row.anchor)"
              >
                <span class="diff-tree-icon" aria-hidden="true">
                  <svg width="12" height="12" viewBox="0 0 16 16">
                    <path
                      fill="none"
                      stroke="currentColor"
                      stroke-width="1.2"
                      d="M3.5 1.5h6l3 3v10h-9v-13z M9.5 1.5v3h3"
                    />
                  </svg>
                </span>
                <span class="tour-file-name" aria-hidden="true">
                  <span class="tour-file-name-stem ellipsis-start">{{ row.filenameStem }}</span>
                  <span v-if="row.filenameSuffix" class="tour-file-name-suffix">{{
                    row.filenameSuffix
                  }}</span>
                </span>
              </button>
              <div
                v-else
                :class="[
                  'diff-tree-row tour-change-row',
                  {
                    active: row.item.anchor === activeAnchor,
                    'tour-change-collapsed': isChangeHidden(row.item),
                  },
                ]"
                :style="{ paddingLeft: `calc(0.375rem + ${row.depth} * 0.85rem)` }"
              >
                <component
                  :is="row.item.trivial ? 'button' : 'span'"
                  :type="row.item.trivial ? 'button' : undefined"
                  class="tour-visibility-control"
                  :title="row.item.trivial ? visibilityLabel(row.item) : undefined"
                  :aria-label="row.item.trivial ? visibilityLabel(row.item) : undefined"
                  :aria-expanded="row.item.trivial ? !isChangeHidden(row.item) : undefined"
                  :aria-controls="row.item.trivial ? row.item.anchor : undefined"
                  @click="toggleVisibility(row.item)"
                >
                  <svg
                    class="tour-visibility-icon"
                    width="12"
                    height="12"
                    viewBox="0 0 24 24"
                    fill="none"
                    stroke="currentColor"
                    stroke-width="1.5"
                    stroke-linecap="round"
                    stroke-linejoin="round"
                    aria-hidden="true"
                  >
                    <path
                      v-if="isChangeHidden(row.item)"
                      d="M3 10q9 10 18 0 M5 12l-2 2 M12 15v3 M19 12l2 2"
                    />
                    <template v-else>
                      <path d="M2 12s3.5-6 10-6 10 6 10 6-3.5 6-10 6S2 12 2 12Z" />
                      <circle cx="12" cy="12" r="2.5" />
                    </template>
                  </svg>
                </component>
                <button
                  type="button"
                  class="tour-change-link"
                  :data-tour-target="row.item.anchor"
                  :title="changeLabel(row.item)"
                  :aria-label="changeLabel(row.item)"
                  :aria-current="row.item.anchor === activeAnchor ? 'location' : undefined"
                  @click="emit('select', row.item.anchor)"
                >
                  <span class="diff-tree-decoration tour-change-range" aria-hidden="true">
                    {{ row.item.decoration || "File change" }}
                  </span>
                  <span
                    v-if="row.item.additions > 0 || row.item.deletions > 0"
                    class="diff-tree-changes"
                    aria-hidden="true"
                  >
                    <span v-if="row.item.additions > 0" class="diff-tree-changes-added"
                      >+{{ row.item.additions }}</span
                    >
                    <span v-if="row.item.deletions > 0" class="diff-tree-changes-deleted"
                      >&minus;{{ row.item.deletions }}</span
                    >
                  </span>
                </button>
              </div>
            </div>
          </div>
        </li>
      </template>
    </ol>
  </nav>
</template>

<script setup lang="ts">
import { computed } from "vue";
import {
  buildTourContentsLayout,
  type TourChangeItem,
  type TourContentsGroup,
  type TourContentsItem,
} from "./commitTourContents";

const props = defineProps<{
  items: TourContentsItem[];
  activeAnchor: string | null;
  expandedAnchors: Set<string>;
}>();
const emit = defineEmits<{
  (e: "select", anchor: string): void;
  (e: "expand-change", anchor: string, expanded: boolean): void;
}>();

const layout = computed(() => buildTourContentsLayout(props.items));

function isChangeHidden(item: TourChangeItem): boolean {
  return item.trivial && !props.expandedAnchors.has(item.anchor);
}

function visibilityLabel(item: TourChangeItem): string {
  return `${isChangeHidden(item) ? "Show" : "Hide"} ${item.label}`;
}

function toggleVisibility(item: TourChangeItem) {
  if (item.trivial) emit("expand-change", item.anchor, isChangeHidden(item));
}

function changeLabel(item: TourChangeItem): string {
  if (!item.trivial) return item.label;
  return `${item.label} — trivial change (${isChangeHidden(item) ? "hidden" : "shown"})`;
}

function groupKey(group: TourContentsGroup): string {
  return group.section?.anchor ?? group.rows[0]?.key ?? "empty";
}
</script>
