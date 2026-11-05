<!-- A header's title that a click renames in place (see NameField). Not
     `editable`, it is plain text. -->
<template>
  <span v-if="editing" class="editable-title-field">
    <NameField
      :initial="text"
      :label="label"
      :placeholder="placeholder"
      :check="check"
      :submit="submit"
      @done="editing = false"
    />
  </span>
  <button
    v-else-if="editable"
    type="button"
    class="editable-title"
    :title="label"
    @click="editing = true"
  >
    {{ text }}
  </button>
  <template v-else>{{ text }}</template>
</template>

<script setup lang="ts">
import { ref } from "vue";
import NameField from "./NameField.vue";

defineProps<{
  text: string;
  /** The button's tooltip and the field's label. */
  label: string;
  editable: boolean;
  placeholder?: string;
  check?: (name: string) => string;
  submit: (name: string) => Promise<void>;
}>();
const editing = ref(false);
</script>
