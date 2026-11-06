// Slack mrkdwn, as Slack users write it and the Slack Assistant posts it,
// rendered to safe HTML: every piece of text is escaped, and only http(s) and
// mailto links are made. See https://api.slack.com/reference/surfaces/formatting.

/** Who and what Slack IDs name, for mentions and channel references. */
export interface SlackNames {
  users: ReadonlyMap<string, string>;
  /** "#name", or "DM". */
  channels: ReadonlyMap<string, string>;
  /** The Slack app's bot user, which mentions of render as @Shelley. */
  botID?: string;
}

const BOT_NAME = "Shelley";

// Common shortcodes; others stay :name:.
const EMOJI = new Map(
  `
+1 👍  thumbsup 👍  -1 👎  thumbsdown 👎  eyes 👀  eye 👁️  tada 🎉  white_check_mark ✅
heavy_check_mark ✔️  ballot_box_with_check ☑️  x ❌  negative_squared_cross_mark ❎  warning ⚠️
no_entry ⛔  no_entry_sign 🚫  stop_sign 🛑  rotating_light 🚨  construction 🚧  sos 🆘  rocket 🚀
fire 🔥  heart ❤️  blue_heart 💙  green_heart 💚  yellow_heart 💛  purple_heart 💜  orange_heart 🧡
black_heart 🖤  broken_heart 💔  sparkling_heart 💖  100 💯  pray 🙏  raised_hands 🙌  clap 👏
wave 👋  ok_hand 👌  muscle 💪  handshake 🤝  point_right 👉  point_left 👈  point_up ☝️
point_down 👇  v ✌️  crossed_fingers 🤞  raised_hand ✋  hand ✋  fist ✊  punch 👊  facepunch 👊
metal 🤘  call_me_hand 🤙  writing_hand ✍️  face_palm 🤦  facepalm 🤦  shrug 🤷  raising_hand 🙋
bow 🙇  saluting_face 🫡  brain 🧠  thinking_face 🤔  smile 😄  smiley 😃  grinning 😀  grin 😁
laughing 😆  joy 😂  rolling_on_the_floor_laughing 🤣  sweat_smile 😅  slightly_smiling_face 🙂
upside_down_face 🙃  wink 😉  blush 😊  innocent 😇  heart_eyes 😍  star-struck 🤩  hugging_face 🤗
sunglasses 😎  nerd_face 🤓  face_with_monocle 🧐  neutral_face 😐  expressionless 😑  smirk 😏
unamused 😒  face_with_rolling_eyes 🙄  grimacing 😬  relieved 😌  pensive 😔  sleeping 😴
exploding_head 🤯  partying_face 🥳  melting_face 🫠  confused 😕  worried 😟
slightly_frowning_face 🙁  open_mouth 😮  astonished 😲  flushed 😳  pleading_face 🥺  cry 😢  sob 😭
scream 😱  sweat 😓  disappointed 😞  rage 😡  angry 😠  skull 💀  ghost 👻  robot_face 🤖
see_no_evil 🙈  hankey 💩  poop 💩  zzz 💤  star ⭐  star2 🌟  sparkles ✨  zap ⚡  boom 💥  dizzy 💫
sunny ☀️  cloud ☁️  rainbow 🌈  snowflake ❄️  ocean 🌊  earth_americas 🌎  globe_with_meridians 🌐
seedling 🌱  herb 🌿  cactus 🌵  four_leaf_clover 🍀  trophy 🏆  sports_medal 🏅
first_place_medal 🥇  dart 🎯  confetti_ball 🎊  balloon 🎈  gift 🎁  birthday 🎂  checkered_flag 🏁
triangular_flag_on_post 🚩  coffee ☕  tea 🍵  beer 🍺  beers 🍻  pizza 🍕  popcorn 🍿  bulb 💡
wrench 🔧  hammer 🔨  hammer_and_wrench 🛠️  gear ⚙️  toolbox 🧰  test_tube 🧪  microscope 🔬
lock 🔒  unlock 🔓  key 🔑  shield 🛡️  mag 🔍  mag_right 🔎  link 🔗  paperclip 📎  pushpin 📌
round_pushpin 📍  package 📦  books 📚  book 📖  memo 📝  pencil2 ✏️  page_facing_up 📄  clipboard 📋
file_folder 📁  open_file_folder 📂  wastebasket 🗑️  calendar 📆  date 📅
chart_with_upwards_trend 📈  chart_with_downwards_trend 📉  bar_chart 📊  email 📧  envelope ✉️
inbox_tray 📥  outbox_tray 📤  bell 🔔  no_bell 🔕  mega 📣  loudspeaker 📢  speech_balloon 💬
thought_balloon 💭  computer 💻  desktop_computer 🖥️  keyboard ⌨️  iphone 📱  floppy_disk 💾
camera 📷  art 🎨  scissors ✂️  satellite 📡  battery 🔋  electric_plug 🔌  moneybag 💰  dollar 💵
hourglass ⌛  hourglass_flowing_sand ⏳  stopwatch ⏱️  alarm_clock ⏰  bug 🐛  bee 🐝  turtle 🐢
snail 🐌  crab 🦀  octopus 🐙  dog 🐶  cat 🐱  unicorn_face 🦄  owl 🦉  red_circle 🔴
large_blue_circle 🔵  large_green_circle 🟢  large_yellow_circle 🟡  large_orange_circle 🟠
white_circle ⚪  black_circle ⚫  question ❓  grey_question ❔  exclamation ❗
heavy_exclamation_mark ❗  bangbang ‼️  heavy_plus_sign ➕  heavy_minus_sign ➖  arrow_right ➡️
arrow_left ⬅️  arrow_up ⬆️  arrow_down ⬇️  arrow_forward ▶️  arrows_counterclockwise 🔄
repeat 🔁  leftwards_arrow_with_hook ↩️  recycle ♻️  ok 🆗  new 🆕  cool 🆒  one 1️⃣  two 2️⃣
three 3️⃣  four 4️⃣  five 5️⃣
`
    .trim()
    .split(/\s+/)
    .flatMap((w, i, all): [string, string][] => (i % 2 ? [] : [[w, all[i + 1]]])),
);

/** The emoji a shortcode (with or without colons, and any skin tone) names,
 * or ":name:" if it is not a common one. */
export function slackEmoji(name: string): string {
  const bare = name.replace(/^:|:$/g, "").replace(/::skin-tone-\d$/, "");
  return EMOJI.get(bare) ?? `:${bare}:`;
}

const SHORTCODE = /:([a-z0-9_+'-]+):(?::skin-tone-\d:)?/g;

/** emojify replaces the common shortcodes in plain text. */
export function emojify(text: string): string {
  return text.replace(SHORTCODE, (_, name: string) => slackEmoji(name));
}

function escapeHTML(s: string): string {
  return s.replace(
    /[&<>"']/g,
    (c) => ({ "&": "&amp;", "<": "&lt;", ">": "&gt;", '"': "&quot;", "'": "&#39;" })[c]!,
  );
}

// Slack escapes these three, and only these, in message text.
function decodeEntities(s: string): string {
  return s.replace(/&(amp|lt|gt);/g, (_, e: string) => ({ amp: "&", lt: "<", gt: ">" })[e]!);
}

type Token =
  | { kind: "link"; href: string; label: string }
  | { kind: "mention"; label: string; title: string }
  | { kind: "text"; label: string };

// token reads the inside of a <…> reference: a user or channel mention, a
// special mention (<!here>, <!subteam^…|@team>, <!date^…|fallback>), or a
// link. Anything else (like Vec<T> in a reply) is not a reference.
function token(inner: string, names: SlackNames): Token | null {
  const [target, ...rest] = inner.split("|");
  const label = rest.length ? decodeEntities(rest.join("|")) : "";
  if (/^@[UW][A-Z0-9]+$/.test(target)) {
    const id = target.slice(1);
    const name = id === names.botID ? BOT_NAME : label || names.users.get(id) || id;
    return { kind: "mention", label: `@${name}`, title: id };
  }
  if (/^#[CG][A-Z0-9]+$/.test(target)) {
    const id = target.slice(1);
    const name = label || names.channels.get(id)?.replace(/^#/, "") || id;
    return { kind: "mention", label: `#${name}`, title: id };
  }
  if (target.startsWith("!")) {
    const special = target.slice(1);
    if (special.startsWith("date^")) return { kind: "text", label: label || special };
    const name = label.replace(/^@/, "") || special.split("^")[0];
    return { kind: "mention", label: `@${name}`, title: special };
  }
  if (/^(https?:\/\/|mailto:)\S+$/i.test(target)) {
    const href = decodeEntities(target);
    return { kind: "link", href, label: label || href.replace(/^mailto:/i, "") };
  }
  // Other schemes, like tel: or slack:, are not links here.
  if (/^[a-z][a-z0-9+.-]*:\S+$/i.test(target)) return { kind: "text", label: label || target };
  return null;
}

function tokenHTML(t: Token): string {
  switch (t.kind) {
    case "link":
      return `<a href="${escapeHTML(t.href)}" target="_blank" rel="noopener noreferrer" class="text-link">${escapeHTML(t.label)}</a>`;
    case "mention":
      return `<span class="slack-mention" title="${escapeHTML(t.title)}">${escapeHTML(t.label)}</span>`;
    case "text":
      return escapeHTML(t.label);
  }
}

// A bare URL, as a reply may hold. Trailing punctuation ends the sentence,
// not the URL.
const BARE_URL = /\bhttps?:\/\/[^\s<>]+/g;

function trimURL(url: string): string {
  for (;;) {
    const last = url[url.length - 1];
    if (/[.,;:!?'"*_~]/.test(last) || (last === ")" && !url.includes("("))) {
      url = url.slice(0, -1);
    } else {
      return url;
    }
  }
}

// Atoms are rendered HTML set aside as \0<index>\0 while the text around them
// is escaped and formatted.
class Atoms {
  private html: string[] = [];
  hold(html: string): string {
    return `\0${this.html.push(html) - 1}\0`;
  }
  restore(s: string): string {
    return s.replace(/\0(\d+)\0/g, (_, i: string) => this.html[Number(i)]);
  }
}

// *bold*, _italic_ and ~strike~ open after a non-word character and close
// before one, so snake_case and 2*3*4 stay as they are.
const FORMATS: [RegExp, string][] = [
  [/(^|[^\p{L}\p{N}*])\*(?=\S)([^*\n]*?\S)\*(?![\p{L}\p{N}*])/gu, "strong"],
  [/(^|[^\p{L}\p{N}_])_(?=\S)([^_\n]*?\S)_(?![\p{L}\p{N}_])/gu, "em"],
  [/(^|[^\p{L}\p{N}~])~(?=\S)([^~\n]*?\S)~(?![\p{L}\p{N}~])/gu, "del"],
];

function renderInline(raw: string, names: SlackNames): string {
  const atoms = new Atoms();
  let s = raw
    .replace(/`([^`\n]+)`/g, (_, code: string) =>
      atoms.hold(`<code class="inline-code">${escapeHTML(decodeEntities(code))}</code>`),
    )
    .replace(/<([^<>\s][^<>\n]*)>/g, (m, inner: string) => {
      const t = token(inner, names);
      return t ? atoms.hold(tokenHTML(t)) : m;
    })
    .replace(BARE_URL, (m) => {
      const url = trimURL(m);
      const href = decodeEntities(url);
      return atoms.hold(tokenHTML({ kind: "link", href, label: href })) + m.slice(url.length);
    })
    .replace(SHORTCODE, (_, name: string) => atoms.hold(escapeHTML(slackEmoji(name))));
  s = escapeHTML(decodeEntities(s));
  for (const [re, tag] of FORMATS) s = s.replace(re, `$1<${tag}>$2</${tag}>`);
  return atoms.restore(s);
}

// Quoted lines start with > (&gt; as Slack sends it); >>> quotes the rest.
const QUOTE = /^(?:>|&gt;) ?/;
const QUOTE_REST = /^(?:>|&gt;){3} ?/;

function renderLines(text: string, names: SlackNames, out: string[]) {
  const lines = text.split("\n");
  let inline: string[] = [];
  let quote: string[] | null = null;
  const flush = () => {
    if (inline.length) out.push(inline.join("\n"));
    if (quote) out.push(`<blockquote>${quote.join("\n")}</blockquote>`);
    inline = [];
    quote = null;
  };
  for (let i = 0; i < lines.length; i++) {
    const line = lines[i];
    if (QUOTE_REST.test(line)) {
      flush();
      const rest = [line.replace(QUOTE_REST, ""), ...lines.slice(i + 1)];
      quote = rest.map((l) => renderInline(l, names));
      break;
    }
    if (QUOTE.test(line)) {
      if (inline.length) flush();
      (quote ??= []).push(renderInline(line.replace(QUOTE, ""), names));
    } else {
      if (quote) flush();
      inline.push(renderInline(line, names));
    }
  }
  flush();
}

/** renderMrkdwn renders Slack mrkdwn to HTML, for an element with
 * white-space: pre-wrap, where quotes and code blocks are blocks. */
export function renderMrkdwn(text: string, names: SlackNames): string {
  const parts = text.replace(/\0/g, "").split("```");
  // An unclosed fence is just backticks.
  if (parts.length % 2 === 0) {
    const last = parts.pop()!;
    parts.push(parts.pop()! + "```" + last);
  }
  const out: string[] = [];
  parts.forEach((part, i) => {
    if (i % 2) {
      const code = part.replace(/^\n/, "").replace(/\n$/, "");
      out.push(
        `<pre class="inline-code-block"><code>${escapeHTML(decodeEntities(code))}</code></pre>`,
      );
      return;
    }
    // A newline beside a code block only separates it.
    if (i > 0) part = part.replace(/^\n/, "");
    if (i < parts.length - 1) part = part.replace(/\n$/, "");
    if (part) renderLines(part, names, out);
  });
  return out.join("");
}

/** mrkdwnText is the text a reader sees, on one line: for excerpts. */
export function mrkdwnText(text: string, names: SlackNames): string {
  let s = text
    .replace(/```/g, " ")
    .replace(/<([^<>\s][^<>\n]*)>/g, (m, inner: string) => token(inner, names)?.label ?? m)
    .replace(SHORTCODE, (_, name: string) => slackEmoji(name));
  s = decodeEntities(s);
  for (const [re] of FORMATS) s = s.replace(re, "$1$2");
  return s.replace(/`/g, "").replace(/\s+/g, " ").trim();
}

/** excerpt shortens text to at most max characters. */
export function excerpt(text: string, max = 80): string {
  const chars = Array.from(text);
  return chars.length > max
    ? chars
        .slice(0, max - 1)
        .join("")
        .trimEnd() + "…"
    : text;
}
