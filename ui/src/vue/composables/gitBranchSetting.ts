import { ref } from "vue";
import { api } from "../../services/api";

const showGitBranch = ref(window.__SHELLEY_INIT__?.show_git_branch === true);

export function useGitBranchSetting() {
  return {
    showGitBranch,
    async setShowGitBranch(show: boolean) {
      await api.setSetting("show_git_branch", String(show));
      showGitBranch.value = show;
    },
  };
}
