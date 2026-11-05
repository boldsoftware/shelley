import type { TranslationKeys } from "../i18n/types";
import type { ConversationVM } from "../services/workspace";

export type VMTone = "progress" | "success" | "danger" | "neutral" | "warning";
type Translate = (key: keyof TranslationKeys) => string;

/** What a conversation works on, before its VM's name: "Works on shared VM". */
export function vmPlace(vm: ConversationVM, t: Translate): string {
  if (vm.status === "deleted") return t("vmPlaceDeleted");
  return t(vm.shared ? "vmPlaceShared" : "vmPlaceOwn");
}

/** A conversation VM's state in words, and its color. */
export function vmStatus(vm: ConversationVM, t: Translate): { label: string; tone: VMTone } {
  switch (vm.status) {
    case "pending":
      return { label: t("vmStarting"), tone: "progress" };
    case "ready":
      return { label: t("vmConnected"), tone: "success" };
    case "failed":
      return { label: t("vmUnreachable"), tone: "danger" };
    case "deleting":
      return vm.error
        ? { label: t("vmDeletionFailed"), tone: "danger" }
        : { label: t("vmDeletingArchived"), tone: "progress" };
    case "deleted":
      return {
        label: vm.conversation_archived
          ? t("vmDeletedArchived")
          : t("vmDeletedRecopy").replace("{vm}", vm.base_vm),
        tone: "neutral",
      };
    case "kept":
      return { label: t("vmNotDeleted"), tone: "warning" };
  }
}
