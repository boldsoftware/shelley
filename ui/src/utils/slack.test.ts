import assert from "node:assert/strict";
import type { Message } from "../types";
import {
  clickedLabel,
  fileName,
  parseSlackEvent,
  slackBlocks,
  sameSlackDirectory,
  slackDirectory,
  slackEventNames,
  slackEventOf,
  slackEventSummary,
} from "./slack";

// As server/slack_channel.go's slackEvent.tagged writes them.
const philip =
  '<slack_message channel="C0A6VAJ9K6Y" channel_name="#temp" thread_ts="1793996286.766169" ts="1793996304.543469" user="U09RYSZS685" user_name="philip" mentions_you="true">\n<@U0CFLGS9HP0> what\'s up\n</slack_message>';
assert.deepEqual(parseSlackEvent(philip), {
  kind: "message",
  channel: "C0A6VAJ9K6Y",
  channelName: "#temp",
  threadTS: "1793996286.766169",
  ts: "1793996304.543469",
  user: "U09RYSZS685",
  userName: "philip",
  mentionsYou: true,
  emoji: "",
  text: "<@U0CFLGS9HP0> what's up",
  files: [],
});

// Attributes are HTML-escaped and Go-quoted; the body is neither.
const escaped = parseSlackEvent(
  '<slack_message channel="D1" channel_name="DM" thread_ts="1.0" ts="1.0" user="U1" user_name="Al &amp; &#34;Bo&#34; &lt;x&gt; \\\\o/ \\u00e9" mentions_you="false">\na &amp; </slack_message> b\n\n[/tmp/shelley-uploads/build.log] [/tmp/shelley-uploads/shot one_0123456789abcdef.png]\n</slack_message>',
);
assert.equal(escaped?.userName, 'Al & "Bo" <x> \\o/ \u00e9');
assert.equal(escaped?.text, "a &amp; </slack_message> b");
assert.deepEqual(escaped?.files, [
  "/tmp/shelley-uploads/build.log",
  "/tmp/shelley-uploads/shot one_0123456789abcdef.png",
]);
assert.equal(fileName(escaped!.files[1]), "shot one.png");

// Files alone.
assert.deepEqual(
  parseSlackEvent(
    '<slack_message channel="C1" channel_name="" thread_ts="1.0" ts="1.0" user="U1" user_name="" mentions_you="false">\n[/tmp/a.png]\n</slack_message>',
  )?.files,
  ["/tmp/a.png"],
);

const reaction = parseSlackEvent(
  '<slack_reaction channel="C1" channel_name="#temp" thread_ts="1.0" ts="1.5" user="U1" user_name="philip" emoji="eyes">\nDeploy? [/not/a/file]\n</slack_reaction>',
);
assert.equal(reaction?.emoji, "eyes");
assert.equal(reaction?.text, "Deploy? [/not/a/file]");
assert.equal(slackEventSummary(reaction!, slackEventNames(reaction!)), "philip reacted 👀");

const action = parseSlackEvent(
  '<slack_action channel="C1" channel_name="#temp" thread_ts="1.0" ts="1.5" user="U1" user_name="philip">\nClicked "Re \\"fresh\\"" (action_id "refresh", value "x")\n</slack_action>',
);
assert.equal(clickedLabel(action!.text), 'Re "fresh"');
assert.equal(clickedLabel("Clicked something"), "Clicked something");
assert.equal(slackEventSummary(action!, slackEventNames(action!)), 'philip clicked Re "fresh"');

// Anything but one well-formed tag is not an event.
for (const text of [
  `${philip}\n`,
  `hi ${philip}`,
  philip.replace("</slack_message>", "</slack_reaction>"),
  philip.replace(' mentions_you="true"', ""),
  philip.replace('user="U09RYSZS685"', 'user="U1" user="U2"'),
  philip.replace("<slack_message ", "<slack_other ").replace("</slack_message>", "</slack_other>"),
  '<slack_reaction channel="C1" channel_name="" thread_ts="1" ts="1" user="U1" user_name="">\nx\n</slack_reaction>',
  "<slack_message>\nx\n</slack_message>",
]) {
  assert.equal(parseSlackEvent(text), null, text);
}

function message(id: string, text: string, external = true): Message {
  return {
    message_id: id,
    conversation_id: "c",
    sequence_id: 1,
    type: "user",
    created_at: "",
    generation: 1,
    llm_data: JSON.stringify({ Role: 0, Content: [{ ID: "", Type: 2, Text: text }] }),
    external_message_id: external ? "x" : undefined,
  };
}

// Only a channel's messages are events.
assert.equal(slackEventOf(message("typed", philip, false)), null);
assert.equal(slackEventOf(message("m1", philip))?.userName, "philip");

// The directory names users and channels, and finds the bot: the leading
// mention of a message that mentions it, unless that is someone who wrote.
const dir = slackDirectory([
  message("d1", philip.replace("<@U0CFLGS9HP0>", "<@U09RYSZS685>")),
  message("d2", philip),
  message(
    "d3",
    philip.replace('channel="C0A6VAJ9K6Y" channel_name="#temp"', 'channel="C2" channel_name=""'),
  ),
]);
assert.equal(dir.botID, "U0CFLGS9HP0");
assert.deepEqual([...dir.users], [["U09RYSZS685", "philip"]]);
assert.deepEqual([...dir.channels], [["C0A6VAJ9K6Y", "#temp"]]);
// A message that changes nothing makes the same directory.
const thread = [message("s1", philip), message("s2", philip.replace("1793996304", "1793996305"))];
const typed = message("s3", "hi", false);
assert.ok(sameSlackDirectory(slackDirectory(thread), slackDirectory([...thread, typed])));
assert.ok(!sameSlackDirectory(slackDirectory(thread.slice(0, 1)), slackDirectory(thread)));
// Mentions labeled with names name, too, unless the events do.
const labeled = slackDirectory([
  message("l1", philip.replace("what's up", "ask <@U2|bob> in <#C2|ops> or <#C0A6VAJ9K6Y|old>")),
]);
assert.deepEqual(
  [...labeled.channels],
  [
    ["C0A6VAJ9K6Y", "#temp"],
    ["C2", "#ops"],
  ],
);
assert.deepEqual(
  [...labeled.users],
  [
    ["U09RYSZS685", "philip"],
    ["U2", "bob"],
  ],
);
assert.equal(dir.messages.get("C0A6VAJ9K6Y/1793996304.543469")?.text, "<@U0CFLGS9HP0> what's up");
assert.equal(
  slackEventSummary(dir.messages.get("C0A6VAJ9K6Y/1793996304.543469")!, dir),
  "philip: @Shelley what's up",
);

// Block Kit, as far as a preview goes.
assert.deepEqual(
  slackBlocks([
    { type: "header", text: { type: "plain_text", text: "Deploy :rocket:" } },
    {
      type: "section",
      text: { type: "mrkdwn", text: "*ok*" },
      fields: [{ type: "plain_text", text: "a" }, {}],
      accessory: {
        type: "button",
        text: { type: "plain_text", text: "Open" },
        url: "https://x.dev",
      },
    },
    { type: "divider" },
    {
      type: "context",
      elements: [
        { type: "mrkdwn", text: "_c_" },
        { type: "image", image_url: "https://i", alt_text: "i" },
      ],
    },
    { type: "image", image_url: "https://img", alt_text: "chart" },
    {
      type: "actions",
      elements: [
        {
          type: "button",
          text: { text: "Refresh" },
          action_id: "refresh",
          value: "x",
          style: "primary",
        },
      ],
    },
    { type: "rich_text" },
    "junk",
  ]),
  [
    { type: "header", text: "Deploy :rocket:" },
    {
      type: "section",
      text: { text: "*ok*", mrkdwn: true },
      fields: [{ text: "a", mrkdwn: false }],
      accessory: {
        type: "button",
        text: "Open",
        url: "https://x.dev",
        style: "",
        title: "https://x.dev",
      },
    },
    { type: "divider" },
    {
      type: "context",
      elements: [
        { type: "text", text: { text: "_c_", mrkdwn: true } },
        { type: "image", url: "https://i", alt: "i" },
      ],
    },
    { type: "image", url: "https://img", alt: "chart", title: "" },
    {
      type: "actions",
      elements: [
        {
          type: "button",
          text: "Refresh",
          url: "",
          style: "primary",
          title: "action_id refresh, value x",
        },
      ],
    },
    { type: "other", name: "rich_text" },
    { type: "other", name: "block" },
  ],
);
assert.deepEqual(slackBlocks({ not: "blocks" }), []);

console.log("slack tests passed");
