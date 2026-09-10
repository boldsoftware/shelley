<!-- Collapsible model-context metadata with expandable skill and tool cards. -->
<template>
  <div v-if="systemPromptText" class="system-prompt-view">
    <button
      type="button"
      class="system-prompt-header"
      :aria-expanded="isExpanded"
      @click="isExpanded = !isExpanded"
    >
      <span class="system-prompt-summary">
        <span class="system-prompt-icon" aria-hidden="true">📋</span>
        <span class="system-prompt-copy">
          <span class="system-prompt-label">System Prompt:</span>
          <span class="system-prompt-meta">
            <span>{{ countLabel(tools.length, "tool") }}</span>
            <span aria-hidden="true">,</span>
            <span>{{ countLabel(skills.length, "skill") }}</span>
          </span>
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
          <article
            v-for="tool in tools"
            :key="tool.name"
            class="system-prompt-card system-prompt-tool-item"
            :class="{
              'system-prompt-card--expanded': isCardExpanded('tool', tool.name),
            }"
          >
            <button
              type="button"
              class="system-prompt-card-summary"
              :aria-expanded="isCardExpanded('tool', tool.name)"
              @click="toggleCard('tool', tool.name)"
            >
              <span class="system-prompt-card-copy">
                <code class="system-prompt-card-name system-prompt-tool-name">
                  {{ tool.name }}
                </code>
                <span class="system-prompt-card-preview">{{ toolPreview(tool) }}</span>
              </span>
              <ToolChevron :expanded="isCardExpanded('tool', tool.name)" />
              <span class="sr-only">
                {{ isCardExpanded("tool", tool.name) ? "Collapse" : "Expand" }}
              </span>
            </button>

            <div v-if="isCardExpanded('tool', tool.name)" class="system-prompt-card-detail">
              <p v-if="tool.description.trim()" class="system-prompt-card-full-description">
                {{ tool.description.trim() }}
              </p>
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

              <div v-if="Object.keys(propsOf(tool)).length > 0" class="system-prompt-tool-params">
                <div class="system-prompt-tool-params-label">Parameters</div>
                <table class="system-prompt-tool-params-table">
                  <tbody>
                    <tr
                      v-for="[paramName, prop] in Object.entries(propsOf(tool))"
                      :key="paramName"
                      class="system-prompt-tool-param-row"
                    >
                      <td class="system-prompt-tool-param-name">
                        <code>{{ paramName }}</code>
                        <span
                          v-if="requiredOf(tool).has(paramName)"
                          class="system-prompt-tool-param-required"
                          >*</span
                        >
                      </td>
                      <td class="system-prompt-tool-param-type">
                        <code>{{ typeLabel(prop) }}</code>
                      </td>
                      <td class="system-prompt-tool-param-desc">
                        <span v-if="prop.description">{{ prop.description }}</span>
                        <span
                          v-if="prop.enum && prop.enum.length > 0"
                          class="system-prompt-tool-param-enum"
                        >
                          {{ " " }}Allowed values:{{ " " }}
                          <template v-for="(value, index) in prop.enum" :key="index">
                            <template v-if="index > 0">, </template>
                            <code>{{ String(value) }}</code>
                          </template>
                        </span>
                      </td>
                    </tr>
                  </tbody>
                </table>
              </div>
            </div>
          </article>
        </div>
      </section>

      <pre class="system-prompt-text">{{ systemPromptText }}</pre>
    </div>
  </div>
</template>

<script setup lang="ts">
import { computed, reactive, ref } from "vue";
import type { Message, LLMContent } from "../../types";
import { extractSystemPromptSkills, type SystemPromptSkill } from "./systemPromptSkills";
import ToolChevron from "./tools/ToolChevron.vue";

interface JSONSchemaProperty {
  type?: string | string[];
  description?: string;
  enum?: (string | number | boolean | null)[];
  items?: JSONSchemaProperty;
  $ref?: string;
  [key: string]: unknown;
}

interface JSONSchema {
  type?: string;
  properties?: Record<string, JSONSchemaProperty>;
  required?: string[];
  [key: string]: unknown;
}

interface ToolDescription {
  name: string;
  description: string;
  parameters?: JSONSchema;
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

function requiredOf(tool: ToolDescription): Set<string> {
  return new Set(tool.parameters?.required ?? []);
}

function propsOf(tool: ToolDescription): Record<string, JSONSchemaProperty> {
  return tool.parameters?.properties ?? {};
}

function typeLabel(prop: JSONSchemaProperty): string {
  return Array.isArray(prop.type) ? prop.type.join(" | ") : (prop.type ?? "");
}

function metadataEntries(skill: SystemPromptSkill): [string, string][] {
  return Object.entries(skill.metadata ?? {});
}

const systemPromptText = computed<string>(() => {
  if (!props.message.llm_data) return "";
  try {
    const llmData =
      typeof props.message.llm_data === "string"
        ? JSON.parse(props.message.llm_data)
        : props.message.llm_data;
    if (llmData && llmData.Content && Array.isArray(llmData.Content)) {
      const textContent = llmData.Content.find(
        (content: LLMContent) => content.Type === 2 && content.Text,
      );
      if (textContent) return textContent.Text;
    }
  } catch (err) {
    console.error("Failed to parse system prompt:", err);
  }
  return "";
});

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
