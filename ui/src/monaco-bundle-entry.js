// Custom Monaco bundle entry. We re-export everything from editor.main
// (the full standalone bundle) plus a few internal modules that monaco-vim
// reaches into. Keeping this in one bundle avoids loading two copies of
// Monaco at runtime when vim mode is enabled.
export * from "monaco-editor/esm/vs/editor/editor.main";
export { ShiftCommand } from "monaco-editor/esm/vs/editor/common/commands/shiftCommand";
