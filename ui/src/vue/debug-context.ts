// Entry for /debug/context/: mounts ContextGraphPage.
import { createApp } from "vue";
import ContextGraphPage from "./components/ContextGraphPage.vue";

const root = document.getElementById("root");
if (!root) throw new Error("Root container not found");
createApp(ContextGraphPage).mount(root);
