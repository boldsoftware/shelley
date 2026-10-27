<!-- Vue port of components/ConfigFieldInput.tsx. Generic config field renderer
     used by NotificationsModal. Migrated from bare <select>/<input> to PrimeVue
     <Select>/<InputText>. Preserves the form-group wrapper, the `config-<name>`
     id (so <label for> keeps working), aria-describedby wiring and the
     config-field-description class. Styling comes from PrimeVue's own token
     system via configFieldDt (see that file) rather than the .form-input CSS. -->
<template>
  <div class="form-group">
    <label :for="inputId"> {{ field.label }}{{ field.required ? " *" : "" }} </label>
    <!-- PrimeVue's Select has no ariaDescribedby prop and sets
         inheritAttrs: false, so a plain :aria-describedby would fall onto the
         root <div> rather than the combobox. input-id puts inputId on the
         .p-select-label span (which carries role="combobox"), so we describe
         it there too via pt.label. -->
    <Select
      v-if="field.options && field.options.length > 0"
      :input-id="inputId"
      :model-value="value || null"
      :options="field.options"
      placeholder="Select..."
      show-clear
      fluid
      :dt="selectFieldDt"
      :pt="field.description ? { label: { 'aria-describedby': descId } } : undefined"
      @update:model-value="emit('change', $event ?? '')"
    />
    <InputText
      v-else
      :id="inputId"
      :type="field.type === 'password' ? 'password' : 'text'"
      :model-value="value"
      :placeholder="clearSaved ? '' : savedSecret ? savedPlaceholder : field.placeholder"
      :disabled="clearSaved"
      fluid
      :dt="inputFieldDt"
      :aria-describedby="field.description ? descId : undefined"
      @update:model-value="emit('change', $event ?? '')"
    />
    <span v-if="field.description" :id="descId" class="config-field-description">
      {{ field.description }}
    </span>
    <div v-if="savedSecret && !field.required" class="form-checkbox">
      <label>
        <input
          type="checkbox"
          :checked="clearSaved"
          @change="emit('clear', ($event.target as HTMLInputElement).checked)"
        />
        Clear saved credential
      </label>
    </div>
  </div>
</template>

<script setup lang="ts">
import { computed } from "vue";
import Select from "primevue/select";
import InputText from "primevue/inputtext";
import { inputFieldDt, selectFieldDt } from "./configFieldDt";

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
  savedSecret: boolean;
  clearSaved: boolean;
}>();
const emit = defineEmits<{
  (e: "change", value: string): void;
  (e: "clear", value: boolean): void;
}>();

const inputId = computed(() => `config-${props.field.name}`);
const descId = computed(() => `${inputId.value}-desc`);
const savedPlaceholder = computed(() => {
  const credential = props.field.name === "webhook_url" ? "webhook" : props.field.name;
  return `Leave blank to keep the saved ${credential} on the server`;
});
</script>
