<!-- Collapsible model-context metadata with expandable skill and tool cards. -->
<template>
  <div v-if="systemPromptText" class="system-prompt-view">
    <button
      type="button"
      class="system-prompt-header generation-context-item"
      :aria-expanded="isExpanded"
      @click="isExpanded = !isExpanded"
    >
      <span class="generation-context-icon" aria-hidden="true">📋</span>
      <span class="generation-context-copy">
        <span class="generation-context-label">System Prompt:</span>
        <span class="generation-context-value">
          {{ countLabel(tools.length, "tool") }}, {{ countLabel(skills.length, "skill") }}
        </span>
      </span>
      <span class="sr-only">{{ isExpanded ? "Collapse" : "Expand" }}</span>
      <span class="tool-toggle" aria-hidden="true">
        <ToolChevron :expanded="isExpanded" />
      </span>
    </button>

    <div v-if="isExpanded" class="system-prompt-content">
      <section v-if="skills.length > 0" class="system-prompt-section system-prompt-skills">
        <h3 class="system-prompt-section-label">✨ Skills ({{ skills.length }})</h3>
        <div class="system-prompt-card-grid">
          <article
            v-for="skill in skills"
            :key="skill.name"
            class="system-prompt-card system-prompt-skill-item"
            :class="{
              'system-prompt-card--expanded': isCardExpanded('skill', skill.name),
            }"
          >
            <button
              type="button"
              class="system-prompt-card-summary"
              :aria-expanded="isCardExpanded('skill', skill.name)"
              @click="toggleCard('skill', skill.name)"
            >
              <span class="system-prompt-card-copy">
                <code class="system-prompt-card-name">{{ skill.name }}</code>
                <span class="system-prompt-card-preview">{{ skill.description }}</span>
              </span>
              <ToolChevron :expanded="isCardExpanded('skill', skill.name)" />
              <span class="sr-only">
                {{ isCardExpanded("skill", skill.name) ? "Collapse" : "Expand" }}
              </span>
            </button>

            <div v-if="isCardExpanded('skill', skill.name)" class="system-prompt-card-detail">
              <p class="system-prompt-card-full-description">{{ skill.description }}</p>
              <dl class="system-prompt-card-metadata">
                <div v-if="skill.source_path || skill.origin" class="system-prompt-card-meta-row">
                  <dt>Source</dt>
                  <dd>
                    <code v-if="skill.source_path">{{ skill.source_path }}</code>
                    <span v-if="skill.origin" class="system-prompt-origin-badge">
                      {{ skill.origin }}
                    </span>
                  </dd>
                </div>
                <div v-if="skill.activate" class="system-prompt-card-meta-row">
                  <dt>Activate</dt>
                  <dd>
                    <code>{{ skill.activate }}</code>
                  </dd>
                </div>
                <div v-if="skill.compatibility" class="system-prompt-card-meta-row">
                  <dt>Compatibility</dt>
                  <dd>{{ skill.compatibility }}</dd>
                </div>
                <div v-if="skill.allowed_tools" class="system-prompt-card-meta-row">
                  <dt>Allowed tools</dt>
                  <dd>
                    <code>{{ skill.allowed_tools }}</code>
                  </dd>
                </div>
                <div v-if="skill.when" class="system-prompt-card-meta-row">
                  <dt>When</dt>
                  <dd>{{ skill.when }}</dd>
                </div>
                <div v-if="skill.license" class="system-prompt-card-meta-row">
                  <dt>License</dt>
                  <dd>{{ skill.license }}</dd>
                </div>
                <div
                  v-for="[key, value] in metadataEntries(skill)"
                  :key="key"
                  class="system-prompt-card-meta-row"
                >
                  <dt>{{ key }}</dt>
                  <dd>{{ value }}</dd>
                </div>
              </dl>
            </div>
          </article>
        </div>
      </section>

      <section v-if="tools.length > 0" class="system-prompt-section system-prompt-tools">
        <h3 class="system-prompt-section-label">🔧 Tools ({{ tools.length }})</h3>
        <div class="system-prompt-card-grid">
          <ToolDescriptionCard
            v-for="tool in tools"
            :key="tool.name"
            v-model:expanded="expandedCards[cardKey('tool', tool.name)]"
            :name="tool.name"
            :description="tool.description"
            :preview="toolPreview(tool)"
            :parameters="tool.parameters"
          >
            <dl class="system-prompt-card-metadata">
              <div v-if="tool.source_path || tool.origin" class="system-prompt-card-meta-row">
                <dt>Source</dt>
                <dd>
                  <code v-if="tool.source_path">{{ tool.source_path }}</code>
                  <span v-if="tool.origin" class="system-prompt-origin-badge">
                    {{ tool.origin }}
                  </span>
                </dd>
              </div>
              <div v-if="tool.type" class="system-prompt-card-meta-row">
                <dt>Type</dt>
                <dd>
                  <code>{{ tool.type }}</code>
                </dd>
              </div>
            </dl>
          </ToolDescriptionCard>
        </div>
      </section>

      <pre class="system-prompt-text">{{ systemPromptText }}</pre>
    </div>
  </div>
</template>

<script setup lang="ts">
import { llmData } from "../../utils/llmData";
import { computed, reactive, ref } from "vue";
import type { Message, LLMContent } from "../../types";
import type { ToolSchema } from "../../services/api";
import { extractSystemPromptSkills, type SystemPromptSkill } from "./systemPromptSkills";
import ToolChevron from "./tools/ToolChevron.vue";
import ToolDescriptionCard from "./ToolDescriptionCard.vue";

interface ToolDescription {
  name: string;
  description: string;
  parameters?: ToolSchema;
  source_path?: string;
  origin?: string;
  type?: string;
  server_side?: boolean;
}

interface SystemPromptDisplayData {
  tools?: ToolDescription[];
  skills?: SystemPromptSkill[];
}

const props = defineProps<{ message: Message }>();

const isExpanded = ref(false);
const expandedCards = reactive<Record<string, boolean>>({});

function countLabel(count: number, noun: string): string {
  return `${count} ${noun}${count === 1 ? "" : "s"}`;
}

function cardKey(kind: "skill" | "tool", name: string): string {
  return `${kind}:${name}`;
}

function isCardExpanded(kind: "skill" | "tool", name: string): boolean {
  return Boolean(expandedCards[cardKey(kind, name)]);
}

function toggleCard(kind: "skill" | "tool", name: string): void {
  const key = cardKey(kind, name);
  expandedCards[key] = !expandedCards[key];
}

function toolPreview(tool: ToolDescription): string {
  return tool.description.trim().split("\n")[0] || "Provider-managed tool.";
}

function metadataEntries(skill: SystemPromptSkill): [string, string][] {
  return Object.entries(skill.metadata ?? {});
}

const systemPromptText = computed<string>(
  () =>
    llmData(props.message)?.Content?.find((content: LLMContent) => content.Type === 2 && content.Text)
      ?.Text ?? "",
);

const displayData = computed<SystemPromptDisplayData>(() => {
  if (!props.message.display_data) return {};
  try {
    return typeof props.message.display_data === "string"
      ? JSON.parse(props.message.display_data)
      : (props.message.display_data as SystemPromptDisplayData);
  } catch (err) {
    console.error("Failed to parse system prompt display data:", err);
    return {};
  }
});

const skills = computed(() =>
  extractSystemPromptSkills(systemPromptText.value, displayData.value.skills ?? []),
);
const tools = computed(() => displayData.value.tools ?? []);
</script>
