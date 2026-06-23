<!-- Vue port of components/ConfigFieldInput.tsx. Generic config field renderer
     used by NotificationsModal. Preserves form-group / form-input classes,
     the `config-<name>` id, aria-describedby wiring and the
     config-field-description class. -->
<template>
  <div class="form-group">
    <label :for="inputId"> {{ field.label }}{{ field.required ? " *" : "" }} </label>
    <select
      v-if="field.options && field.options.length > 0"
      :id="inputId"
      class="form-input"
      :value="value"
      :aria-describedby="field.description ? descId : undefined"
      @change="emit('change', ($event.target as HTMLSelectElement).value)"
    >
      <option value="">Select...</option>
      <option v-for="opt in field.options" :key="opt" :value="opt">{{ opt }}</option>
    </select>
    <input
      v-else
      :id="inputId"
      class="form-input"
      :type="field.type === 'password' ? 'password' : 'text'"
      :value="value"
      :placeholder="field.placeholder"
      :aria-describedby="field.description ? descId : undefined"
      @input="emit('change', ($event.target as HTMLInputElement).value)"
    />
    <span v-if="field.description" :id="descId" class="config-field-description">
      {{ field.description }}
    </span>
  </div>
</template>

<script setup lang="ts">
import { computed } from "vue";

interface ConfigField {
  name: string;
  label: string;
  type: string;
  required: boolean;
  placeholder?: string;
  description?: string;
  options?: string[];
}

const props = defineProps<{
  field: ConfigField;
  value: string;
}>();
const emit = defineEmits<{ (e: "change", value: string): void }>();

const inputId = computed(() => `config-${props.field.name}`);
const descId = computed(() => `${inputId.value}-desc`);
</script>
