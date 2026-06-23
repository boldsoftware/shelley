<!-- Vue port of components/SystemPromptView.tsx. Collapsible system-prompt card
     with a tools list. Preserves the system-prompt-* / tool-toggle /
     tool-chevron / tool-item-chevron class contract and the Collapse/Expand
     aria-labels. The React ToolItem subcomponent is inlined with a per-tool
     expanded-state map keyed by tool name. -->
<template>
  <div v-if="systemPromptText" class="system-prompt-view">
    <div class="system-prompt-header" @click="isExpanded = !isExpanded">
      <div class="system-prompt-summary">
        <span class="system-prompt-icon">📋</span>
        <span class="system-prompt-label">System Prompt</span>
        <span class="system-prompt-meta">
          {{ lineCount }} lines, {{ sizeKb }} KB{{
            tools.length > 0 ? ` · ${tools.length} tools` : ""
          }}
        </span>
      </div>
      <button
        class="tool-toggle"
        :aria-label="isExpanded ? 'Collapse' : 'Expand'"
        :aria-expanded="isExpanded"
      >
        <svg
          width="12"
          height="12"
          viewBox="0 0 12 12"
          fill="none"
          xmlns="http://www.w3.org/2000/svg"
          :class="`tool-chevron${isExpanded ? ' tool-chevron-expanded' : ''}`"
        >
          <path
            d="M4.5 3L7.5 6L4.5 9"
            stroke="currentColor"
            stroke-width="1.5"
            stroke-linecap="round"
            stroke-linejoin="round"
          />
        </svg>
      </button>
    </div>

    <div v-if="isExpanded" class="system-prompt-content">
      <div v-if="tools.length > 0" class="system-prompt-tools">
        <div class="system-prompt-tools-label">🔧 Tools ({{ tools.length }})</div>
        <div class="system-prompt-tools-list">
          <div v-for="tool in tools" :key="tool.name" class="system-prompt-tool-item">
            <div
              :class="`system-prompt-tool-header${hasDetails(tool) ? ' system-prompt-tool-header--clickable' : ''}`"
              :tabindex="hasDetails(tool) ? 0 : undefined"
              :role="hasDetails(tool) ? 'button' : undefined"
              :aria-expanded="hasDetails(tool) ? !!expanded[tool.name] : undefined"
              @click="hasDetails(tool) && toggle(tool.name)"
              @keydown="onHeaderKeydown($event, tool)"
            >
              <svg
                v-if="hasDetails(tool)"
                width="10"
                height="10"
                viewBox="0 0 10 10"
                fill="none"
                xmlns="http://www.w3.org/2000/svg"
                :class="`tool-item-chevron${expanded[tool.name] ? ' tool-item-chevron--expanded' : ''}`"
              >
                <path
                  d="M3 2L7 5L3 8"
                  stroke="currentColor"
                  stroke-width="1.5"
                  stroke-linecap="round"
                  stroke-linejoin="round"
                />
              </svg>
              <span v-else class="tool-item-chevron-spacer" />
              <code class="system-prompt-tool-name">{{ tool.name }}</code>
              <span class="system-prompt-tool-desc">{{ firstLine(tool) }}</span>
            </div>

            <div v-if="expanded[tool.name] && hasDetails(tool)" class="system-prompt-tool-detail">
              <p v-if="tool.description.trim().includes('\n')" class="system-prompt-tool-full-desc">
                {{ tool.description.trim().split("\n").slice(1).join("\n").trim() }}
              </p>
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
                          <template v-for="(v, i) in prop.enum" :key="i">
                            <template v-if="i > 0">, </template>
                            <code>{{ String(v) }}</code>
                          </template>
                        </span>
                      </td>
                    </tr>
                  </tbody>
                </table>
              </div>
            </div>
          </div>
        </div>
      </div>
      <pre class="system-prompt-text">{{ systemPromptText }}</pre>
    </div>
  </div>
</template>

<script setup lang="ts">
import { computed, reactive, ref } from "vue";
import type { Message, LLMContent } from "../../types";

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
}

interface SystemPromptDisplayData {
  tools?: ToolDescription[];
}

const props = defineProps<{ message: Message }>();

const isExpanded = ref(false);
const expanded = reactive<Record<string, boolean>>({});

function toggle(name: string) {
  expanded[name] = !expanded[name];
}

function firstLine(tool: ToolDescription): string {
  return tool.description.trim().split("\n")[0];
}
function hasDetails(tool: ToolDescription): boolean {
  return (
    tool.description.trim().includes("\n") ||
    Boolean(tool.parameters?.properties && Object.keys(tool.parameters.properties).length > 0)
  );
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

function onHeaderKeydown(e: KeyboardEvent, tool: ToolDescription) {
  if (!hasDetails(tool)) return;
  if (e.key === "Enter" || e.key === " ") {
    e.preventDefault();
    toggle(tool.name);
  }
}

const systemPromptText = computed<string>(() => {
  if (!props.message.llm_data) return "";
  try {
    const llmData =
      typeof props.message.llm_data === "string"
        ? JSON.parse(props.message.llm_data)
        : props.message.llm_data;
    if (llmData && llmData.Content && Array.isArray(llmData.Content)) {
      const textContent = llmData.Content.find((c: LLMContent) => c.Type === 2 && c.Text);
      if (textContent) return textContent.Text;
    }
  } catch (err) {
    console.error("Failed to parse system prompt:", err);
  }
  return "";
});

const tools = computed<ToolDescription[]>(() => {
  if (!props.message.display_data) return [];
  try {
    const displayData: SystemPromptDisplayData =
      typeof props.message.display_data === "string"
        ? JSON.parse(props.message.display_data)
        : (props.message.display_data as SystemPromptDisplayData);
    if (displayData && displayData.tools) return displayData.tools;
  } catch (err) {
    console.error("Failed to parse system prompt display data:", err);
  }
  return [];
});

const lineCount = computed(() => systemPromptText.value.split("\n").length);
const sizeKb = computed(() => (systemPromptText.value.length / 1024).toFixed(1));
</script>
