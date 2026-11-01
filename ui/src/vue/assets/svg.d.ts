// The UI build loads .svg imports as their source text (scripts/build.js).
declare module "*.svg" {
  const source: string;
  export default source;
}
