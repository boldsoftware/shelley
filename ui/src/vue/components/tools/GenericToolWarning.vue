<!-- Shared bug indicator for any generic tool fallback. -->
<template>
  <span v-if="uiUpdated" class="generic-tool-update">
    Shelley has been updated. <a :href="pageURL()" @click.stop.prevent="reloadPage">Reload</a> to
    see the UI for this tool result.
  </span>
  <a
    v-else
    :href="reportBugURL"
    :aria-label="`GENERIC TOOL RESULT (Please report a bug) — missing UI for ${reportedToolName} (opens in new tab)`"
    class="generic-tool-warning"
    target="_blank"
    rel="noopener noreferrer"
    @click.stop
    @keydown.space.prevent.stop
  >
    GENERIC TOOL RESULT (Please report a bug)
  </a>
</template>

<script setup lang="ts">
import { computed, inject } from "vue";
import { genericToolReportName, genericToolReportURL } from "../../../utils/genericToolReport";
import { UiUpdatedKey } from "../../composables/uiUpdated";

const props = defineProps<{
  toolName: string;
}>();

const reportedToolName = computed(() => genericToolReportName(props.toolName));
const uiUpdated = inject(UiUpdatedKey);
const pageURL = () => window.location.href;
const reloadPage = () => window.location.reload();
const reportBugURL = computed(() => genericToolReportURL(props.toolName));
</script>
