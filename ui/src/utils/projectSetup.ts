import type { ProjectRepo } from "../services/workspace";

/** The exe.dev VM user's home, where a base VM's setup clones repositories. */
export const VM_HOME = "/home/exedev";

const slug = (name: string) =>
  name
    .toLowerCase()
    .replace(/[^a-z0-9]+/g, "-")
    .replace(/^-|-$/g, "") || "project";

/** A project's working directory for its repositories: none on this host;
 * on a VM, the single repository's clone, home holding several, or a
 * directory named after the project. */
export function projectCwd(name: string, repos: ProjectRepo[], onVM: boolean): string {
  if (!onVM) return "";
  if (repos.length === 1) return `${VM_HOME}/${repos[0].repository.split("/")[1]}`;
  return repos.length ? VM_HOME : `${VM_HOME}/${slug(name)}`;
}

/** name, or name-2, -3, … if a project has it (case-insensitively). */
export function uniqueProjectName(name: string, taken: string[]): string {
  const lower = new Set(taken.map((t) => t.toLowerCase()));
  let candidate = name;
  for (let n = 2; lower.has(candidate.toLowerCase()); n++) candidate = `${name}-${n}`;
  return candidate;
}

/** The Assistant prompt that starts a web app: on its own new exe.dev VM,
 * made public, through exe-dev integration (vmHost, with NAME for the VM's
 * name), or without one on a port of this VM. It ends where the user
 * describes the app. */
export function webAppPrompt(integration: string, vmHost: string): string {
  const steps = integration
    ? `Create a new exe.dev VM for it through the ${integration} integration (\`new --name NAME\`, tagged as the integration requires) and build the app there; \`new --prompt\` can hand the build to the VM's own Shelley. Serve it on port 8000, so the VM's HTTPS proxy serves it at https://${vmHost}/, make it public with \`share set-public NAME\`, check that the page loads, and reply with its URL.`
    : "Build it on this VM, in its own directory under ~/apps, served by a systemd unit on a free port (3000-9999). Check that the page loads, and reply with its URL.";
  return `Build me a web app. ${steps}\n\nThe app: `;
}

/** A link that opens a text to number with body. Messages on Apple
 * devices (iPadOS reports itself as a Mac) takes the body after "&";
 * others take RFC 5724's "?body=". */
export function smsLink(number: string, body: string, userAgent: string): string {
  const apple = /\b(iPhone|iPad|iPod|Macintosh)\b/.test(userAgent);
  return `sms:${number}${apple ? "&" : "?"}body=${encodeURIComponent(body)}`;
}
