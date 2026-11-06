import assert from "node:assert/strict";
import {
  emojify,
  excerpt,
  mrkdwnText,
  renderMrkdwn,
  slackEmoji,
  type SlackNames,
} from "./slackMrkdwn";

const names: SlackNames = {
  users: new Map([["U1", "philip"]]),
  channels: new Map([
    ["C1", "#temp"],
    ["D1", "DM"],
  ]),
  botID: "UBOT",
};
const render = (text: string) => renderMrkdwn(text, names);
const link = (href: string, label: string) =>
  `<a href="${href}" target="_blank" rel="noopener noreferrer" class="text-link">${label}</a>`;

// Mentions: the bot as @Shelley, users and channels by name, else by ID.
assert.equal(
  render("<@UBOT> what's up <@U1> <@U9> <@U2|bob>"),
  '<span class="slack-mention" title="UBOT">@Shelley</span> what&#39;s up ' +
    '<span class="slack-mention" title="U1">@philip</span> ' +
    '<span class="slack-mention" title="U9">@U9</span> ' +
    '<span class="slack-mention" title="U2">@bob</span>',
);
assert.equal(
  render("<#C1> <#C2|general> <#C3> <!here> <!subteam^S1|@eng> <!date^1^{date}|Feb 18>"),
  '<span class="slack-mention" title="C1">#temp</span> ' +
    '<span class="slack-mention" title="C2">#general</span> ' +
    '<span class="slack-mention" title="C3">#C3</span> ' +
    '<span class="slack-mention" title="here">@here</span> ' +
    '<span class="slack-mention" title="subteam^S1">@eng</span> Feb 18',
);

// Links, labeled or not; bare URLs in replies; only http(s) and mailto.
assert.equal(
  render("<https://exe.dev/a?b=1&amp;c=2|the docs> and <https://x.dev>."),
  `${link("https://exe.dev/a?b=1&amp;c=2", "the docs")} and ${link("https://x.dev", "https://x.dev")}.`,
);
assert.equal(render("<mailto:a@b.c|a@b.c>"), link("mailto:a@b.c", "a@b.c"));
assert.equal(
  render("see https://x.dev/p_(q), and https://y.dev."),
  `see ${link("https://x.dev/p_(q)", "https://x.dev/p_(q)")}, and ${link("https://y.dev", "https://y.dev")}.`,
);
assert.equal(render("<javascript:alert(1)|click>"), "click");

// No HTML gets through, in text, labels, URLs or code.
assert.equal(
  render('<img src=x onerror="alert(1)"> &lt;b&gt; `<i>` <https://x.dev/"><script>|<b>x</b>>'),
  "&lt;img src=x onerror=&quot;alert(1)&quot;&gt; &lt;b&gt; " +
    '<code class="inline-code">&lt;i&gt;</code> ' +
    link("https://x.dev/&quot;", "https://x.dev/&quot;") +
    "&lt;script&gt;|&lt;b&gt;x&lt;/b&gt;&gt;",
);
assert.equal(
  render('<https://x.dev/"onclick="a|y>'),
  link("https://x.dev/&quot;onclick=&quot;a", "y"),
);
assert.equal(render("Vec<T> & a<b"), "Vec&lt;T&gt; &amp; a&lt;b");
assert.equal(render("a\0b"), "ab");

// Formatting at word boundaries, not inside words or URLs.
assert.equal(
  render("*bold* _italic_ ~gone~ *<https://x.dev|linked>*"),
  `<strong>bold</strong> <em>italic</em> <del>gone</del> <strong>${link("https://x.dev", "linked")}</strong>`,
);
assert.equal(render("*_both_*"), "<strong><em>both</em></strong>");
assert.equal(
  render("snake_case_name 2*3*4 https://x.dev/a_b_c"),
  `snake_case_name 2*3*4 ${link("https://x.dev/a_b_c", "https://x.dev/a_b_c")}`,
);
assert.equal(
  render("`*not bold*` * spaced *"),
  '<code class="inline-code">*not bold*</code> * spaced *',
);

// Emoji, with skin tones; unknown shortcodes and times stay.
assert.equal(
  render(":eyes: :+1::skin-tone-3: :white_check_mark: :party_parrot: 10:30:45"),
  "👀 👍 ✅ :party_parrot: 10:30:45",
);
assert.equal(slackEmoji("eyes"), "👀");
assert.equal(slackEmoji(":tada:"), "🎉");
assert.equal(slackEmoji("constructor"), ":constructor:");
assert.equal(emojify("Ship it :rocket: <b>"), "Ship it 🚀 <b>");

// Lines, quotes (> as Slack sends it, &gt;) and code blocks.
assert.equal(render("one\n  two"), "one\n  two");
assert.equal(
  render("&gt; quoted *a*\n> more\nafter"),
  "<blockquote>quoted <strong>a</strong>\nmore</blockquote>after",
);
assert.equal(render("&gt;&gt;&gt; all\nof it"), "<blockquote>all\nof it</blockquote>");
assert.equal(
  render("run:\n```\nmake *all* &lt;x&gt;\n```\ndone"),
  'run:<pre class="inline-code-block"><code>make *all* &lt;x&gt;</code></pre>done',
);
assert.equal(render("a ``` b"), "a ``` b");

// Plain text, for excerpts.
assert.equal(
  mrkdwnText("<@UBOT> *deploy* <https://x.dev|it> &amp; :rocket:\n```x```", names),
  "@Shelley deploy it & 🚀 x",
);
assert.equal(excerpt("abcdef", 4), "abc…");
assert.equal(excerpt("abc", 4), "abc");

console.log("slackMrkdwn tests passed");
