import type { InjectionKey, Ref } from "vue";

export const UiUpdatedKey: InjectionKey<Readonly<Ref<boolean>>> = Symbol("uiUpdated");
