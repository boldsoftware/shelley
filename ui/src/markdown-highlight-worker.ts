// Tokenizes fenced chat code blocks in a dedicated worker. The generated
// loader catalog covers every Shiki bundled grammar, but each grammar is only
// imported and registered when a fence actually requests it.
import { createHighlighterCore } from "@shikijs/core";
import { createJavaScriptRegexEngine } from "@shikijs/engine-javascript";
import { SHIKI_LANGUAGE_LOADERS } from "./generated-shiki-language-loaders";
import githubDark from "@shikijs/themes/github-dark";
import githubLight from "@shikijs/themes/github-light";

interface HighlightRequest {
  id: number;
  language: string;
  source: string;
}

interface HighlightToken {
  content: string;
  light: string;
  dark: string;
  fontStyle: number;
}

type HighlightResponse =
  | { id: number; kind: "highlighted"; lines: HighlightToken[][] }
  | { id: number; kind: "unknown" }
  | { id: number; kind: "error"; error: string };

let highlighter: ReturnType<typeof createHighlighterCore> | undefined;
const languageLoads = new Map<string, Promise<void>>();

function getHighlighter(): ReturnType<typeof createHighlighterCore> {
  return (highlighter ??= createHighlighterCore({
    themes: [githubLight, githubDark],
    langs: [],
    engine: createJavaScriptRegexEngine(),
  }));
}

async function loadLanguage(language: string): Promise<boolean> {
  if (!Object.prototype.hasOwnProperty.call(SHIKI_LANGUAGE_LOADERS, language)) return false;
  const loader = SHIKI_LANGUAGE_LOADERS[language as keyof typeof SHIKI_LANGUAGE_LOADERS];

  let load = languageLoads.get(language);
  if (!load) {
    load = (async () => {
      const [instance, registrations] = await Promise.all([getHighlighter(), loader()]);
      await instance.loadLanguage(registrations);
    })();
    languageLoads.set(language, load);
    void load.catch(() => languageLoads.delete(language));
  }
  await load;
  return true;
}

self.addEventListener("message", async ({ data }: MessageEvent<HighlightRequest>) => {
  try {
    if (!(await loadLanguage(data.language))) {
      self.postMessage({ id: data.id, kind: "unknown" } satisfies HighlightResponse);
      return;
    }

    const tokens = (await getHighlighter()).codeToTokensWithThemes(data.source, {
      lang: data.language,
      themes: { light: "github-light", dark: "github-dark" },
    });
    const lines: HighlightToken[][] = tokens.map((line) =>
      line.map((token) => ({
        content: token.content,
        light: token.variants.light.color ?? "",
        dark: token.variants.dark.color ?? "",
        fontStyle: token.variants.light.fontStyle ?? 0,
      })),
    );
    self.postMessage({ id: data.id, kind: "highlighted", lines } satisfies HighlightResponse);
  } catch (error) {
    self.postMessage({
      id: data.id,
      kind: "error",
      error: error instanceof Error ? error.message : String(error),
    } satisfies HighlightResponse);
  }
});
