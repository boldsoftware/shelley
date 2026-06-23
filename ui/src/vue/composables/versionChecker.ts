// Vue port of the useVersionChecker hook from components/VersionChecker.tsx.
// Owns version-check state + the modal open/close flags. The actual modal
// chrome lives in components/VersionChecker.vue; a consumer renders that
// component bound to { isOpen: showModal, versionInfo, isLoading } and wires
// @close to closeModal.
import { onMounted, ref } from "vue";
import { api } from "../../services/api";
import type { VersionInfo } from "../../types";

export interface UseVersionCheckerOptions {
  onUpdateAvailable?: (hasUpdate: boolean) => void;
}

export function useVersionChecker(options: UseVersionCheckerOptions = {}) {
  const { onUpdateAvailable } = options;
  const versionInfo = ref<VersionInfo | null>(null);
  const showModal = ref(false);
  const isLoading = ref(false);
  const shouldNotify = ref(false);

  async function checkVersion() {
    isLoading.value = true;
    try {
      // Always force refresh when checking.
      const info = await api.checkVersion(true);
      versionInfo.value = info;
      shouldNotify.value = info.should_notify;
      onUpdateAvailable?.(info.should_notify);
    } catch (err) {
      console.error("Failed to check version:", err);
    } finally {
      isLoading.value = false;
    }
  }

  // Check version on mount (uses cache).
  onMounted(async () => {
    try {
      const info = await api.checkVersion(false);
      versionInfo.value = info;
      shouldNotify.value = info.should_notify;
      onUpdateAvailable?.(info.should_notify);
    } catch (err) {
      console.error("Failed to check version:", err);
    }
  });

  function openModal() {
    showModal.value = true;
    // Always check for a new version when opening the modal.
    checkVersion();
  }

  function closeModal() {
    showModal.value = false;
  }

  return {
    hasUpdate: shouldNotify, // For red dot indicator (5+ days apart)
    versionInfo,
    showModal,
    isLoading,
    openModal,
    closeModal,
  };
}
