/** errorMessage is what to show for a caught error. */
export function errorMessage(err: unknown): string {
  return err instanceof Error ? err.message : String(err);
}
