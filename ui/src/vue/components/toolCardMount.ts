export type ToolCardPlaceholderKind =
  | "audio"
  | "bash"
  | "generic"
  | "media"
  | "output-iframe"
  | "patch";

export function toolCardPlaceholderKind(
  toolName: string,
  toolInput?: unknown,
  display?: unknown,
): ToolCardPlaceholderKind {
  if (toolName === "bash") return "bash";
  if (toolName === "patch") return "patch";
  if (toolName === "output_html" || toolName === "output_iframe") return "output-iframe";
  if (toolName === "openai_audio_transcription") return "audio";
  if (
    toolName === "screenshot" ||
    toolName === "browser_take_screenshot" ||
    toolName === "read_image" ||
    (toolName === "browser" &&
      typeof toolInput === "object" &&
      toolInput !== null &&
      "action" in toolInput &&
      (toolInput as { action?: unknown }).action === "screenshot") ||
    (toolName === "llm_one_shot" && hasImageOutput(display))
  ) {
    return "media";
  }
  return "generic";
}

function hasImageOutput(display: unknown): boolean {
  if (typeof display !== "object" || display === null) return false;
  const images = (display as { images?: unknown }).images;
  return (
    Array.isArray(images) &&
    images.some(
      (image) =>
        typeof image === "object" &&
        image !== null &&
        typeof (image as { url?: unknown }).url === "string",
    )
  );
}
