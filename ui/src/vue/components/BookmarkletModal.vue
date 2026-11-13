<!-- The Shelley bookmarklet for this Shelley: drag the link to the bookmarks
     bar (see src/bookmarklet/page.ts). -->
<template>
  <Modal
    :is-open="isOpen"
    :title="t('bookmarklet')"
    class-name="bookmarklet-modal"
    @close="emit('close')"
  >
    <p class="bookmarklet-help">{{ t("bookmarkletHelp") }}</p>
    <a class="bookmarklet-link" :href="href" data-testid="bookmarklet-link" @click.prevent
      ><i class="pi pi-bookmark" aria-hidden="true" />Shelley</a
    >
    <template v-if="bookmarkletSites.length">
      <p class="bookmarklet-help bookmarklet-sites-title">{{ t("bookmarkletSites") }}</p>
      <ul class="bookmarklet-sites" data-testid="bookmarklet-sites">
        <li v-for="site in bookmarkletSites" :key="site">
          <span>{{ site }}</span>
          <Button
            size="small"
            severity="secondary"
            text
            :label="t('removeAction')"
            @click="forgetBookmarkletSite(site)"
          />
        </li>
      </ul>
    </template>
  </Modal>
</template>

<script setup lang="ts">
import Button from "primevue/button";
import Modal from "./Modal.vue";
import { useI18n } from "../composables/i18n";
import { bookmarkletSites, forgetBookmarkletSite } from "../composables/bookmarklet";

defineProps<{ isOpen: boolean; href: string }>();
const emit = defineEmits<{ (e: "close"): void }>();
const { t } = useI18n();
</script>
