// Custom Monaco bundle entry. We re-export everything from editor.main
// (the full standalone bundle) plus monaco-vim, which services/monaco.ts
// loads from here: bundled into main.js, its imports of Monaco would load
// Monaco on every page, and here they bind to this bundle's Monaco rather
// than a second copy.
export * from "monaco-editor/esm/vs/editor/editor.main";
export { initVimMode, VimMode, StatusBar } from "monaco-vim";
