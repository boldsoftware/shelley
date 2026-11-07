<!-- llm_one_shot tool: the prompt files and model in the header, images the
     model produced always visible below it, the request and result in the
     details. -->
<template>
  <ToolCard emoji="🤖" :complete="complete" :is-running="isRunning" :tool-invoked-at="toolInvokedAt">
    <template #summary>
      <span class="tool-name">llm_one_shot</span>
      <span class="tool-command">{{ summary }}</span>
    </template>

    <template #always>
      <div v-if="displayImages.length" class="tool-section llm-one-shot-images">
        <div v-for="img in displayImages" :key="img.url" class="screenshot-tool-image-container">
          <CommentableImage
            :src="img.url"
            :alt="`Image: ${img.path || 'attachment'}`"
            :path="img.path"
            :width="img.width"
            :height="img.height"
            :source-width="img.source_width"
            :source-height="img.source_height"
            :needs-auto-orient="(img.source_orientation ?? 1) > 1"
          />
        </div>
      </div>
    </template>

    <ToolSection label="Prompt files" :text="promptFiles.join('\n') || '(none)'" />
    <ToolSection v-if="model" label="Model" :text="model" />
    <ToolSection v-if="input.system_prompt" label="System prompt" :text="input.system_prompt" />
    <ToolSection v-if="input.output_file" label="Output file" :text="input.output_file" />
    <ToolSection
      v-if="complete"
      label="Result"
      :text="resultText || '(no output)'"
      :has-error="hasError"
      :execution-time="executionTime"
    />
  </ToolCard>
</template>

<script setup lang="ts">
import { computed } from "vue";
import CommentableImage from "../CommentableImage.vue";
import ToolCard from "./ToolCard.vue";
import ToolSection from "./ToolSection.vue";
import { toolComplete, type ToolProps } from "./toolProps";

interface LLMOneShotInput {
  prompt_files?: string[] | string;
  output_file?: string;
  model?: string;
  system_prompt?: string;
}

interface LLMOneShotDisplayImage {
  url: string;
  path?: string;
  width?: number;
  height?: number;
  // Dimensions of the file at `path`, which exceed width/height when the image
  // was downscaled to fit model limits. Image comments are reported in these.
  source_width?: number;
  source_height?: number;
  // EXIF orientation of that file, present only when it is not 1 (normal): its
  // stored pixels are rotated relative to the dimensions above, so a crop of a
  // commented region has to auto-orient first.
  source_orientation?: number;
}

const props = defineProps<ToolProps>();

const input = computed<LLMOneShotInput>(() =>
  typeof props.toolInput === "object" && props.toolInput !== null
    ? (props.toolInput as LLMOneShotInput)
    : {},
);

const promptFiles = computed(() => {
  const pf = input.value.prompt_files;
  if (Array.isArray(pf)) return pf;
  if (typeof pf === "string" && pf) return [pf];
  return [];
});
const model = computed(() => input.value.model || "");

const displayImages = computed<LLMOneShotDisplayImage[]>(() => {
  const d = props.display;
  if (typeof d !== "object" || d === null) return [];
  const images = (d as { images?: unknown }).images;
  if (!Array.isArray(images)) return [];
  return images.filter(
    (img): img is LLMOneShotDisplayImage =>
      typeof img === "object" && img !== null && typeof (img as { url?: unknown }).url === "string",
  );
});

const resultText = computed(
  () =>
    props.toolResult
      ?.filter((r) => r.Type === 2)
      .map((r) => r.Text)
      .join("\n") || "",
);

const complete = computed(() => toolComplete(props));

const summary = computed(() => {
  const parts: string[] = [];
  if (promptFiles.value.length) parts.push(promptFiles.value.join(", "));
  if (model.value) parts.push(`model: ${model.value}`);
  return parts.join(" · ") || "llm_one_shot";
});
</script>
