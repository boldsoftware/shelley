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

/** Instructions for a project of web apps: each on its own new exe.dev VM
 * (vmHost, with NAME for its name), which its tasks create and reach
 * through exe-dev integration (at host), or, with none (""), each on its
 * own port of this VM. */
export function webAppsInstructions(integration: string, host: string, vmHost: string): string {
  const serve =
    "Serve each app with a systemd unit so it survives reboots. Only make it public when the user asks. Verify the live URL in the browser (screenshot) and report it to the user.";
  if (!integration)
    return `Web apps, each served on its own port (3000-9999) of this VM, in its own directory under ~/apps.\n\n${serve}`;
  return [
    "Web apps, each on its own new exe.dev VM named after the app.",
    `Tasks run on this host and manage VMs through the ${integration} integration, which acts as the user on exe.dev: \`curl https://${host}/\` says what it may do (e.g. a tag-scoped key reaches only VMs with its tag, so tag each new VM with it), and \`curl -X POST https://${host}/exec -d 'COMMAND'\` runs an exe.dev CLI command (\`help new\` lists flags; e.g. \`new --name NAME\`). SSH to a VM through the integration's ssh-agent: keep \`socat UNIX-LISTEN:/tmp/${integration}.sock,fork "EXEC:'openssl s_client -quiet -verify_quiet -verify_return_error -verify_hostname ${host} -alpn ssh-agent -connect ${host}:443'"\` running (e.g. in tmux), then \`SSH_AUTH_SOCK=/tmp/${integration}.sock ssh exedev@${vmHost}\`.`,
    `Serve the app on port 8000 of its VM, at https://${vmHost}/. ${serve} Never modify or delete VMs you did not create.`,
  ].join("\n\n");
}

/** A link that opens a text to number with body. Messages on Apple
 * devices (iPadOS reports itself as a Mac) takes the body after "&";
 * others take RFC 5724's "?body=". */
export function smsLink(number: string, body: string, userAgent: string): string {
  const apple = /\b(iPhone|iPad|iPod|Macintosh)\b/.test(userAgent);
  return `sms:${number}${apple ? "&" : "?"}body=${encodeURIComponent(body)}`;
}
