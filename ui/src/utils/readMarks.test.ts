import { ReadMarks } from "./readMarks";

let passed = 0;
let failed = 0;

function expect(name: string, got: unknown, want: unknown) {
  if (got === want) {
    passed++;
  } else {
    failed++;
    console.error(`FAIL: ${name}: got ${String(got)}, want ${String(want)}`);
  }
}

const row = (id: string, attention?: number) => ({
  conversation_id: id,
  last_attention_at: attention,
});

{
  const s = new ReadMarks();
  expect("nothing is unread before the snapshot", s.unreadTime(row("a", 99)), 0);

  // Page load on b: the first snapshot's marks are read by opening b.
  s.snapshot(
    {
      first_seen_at: 10,
      receipts: [{ conversation_id: "a", read_at: 20 }],
      unread_marks: [{ conversation_id: "b", unread_at: 30 }],
    },
    "b",
  );
  expect("reply before the first visit", s.unreadTime(row("c", 5)), 0);
  expect("reply after the receipt", s.unreadTime(row("a", 25)), 25);
  expect("reply covered by the receipt", s.unreadTime(row("a", 20)), 0);
  expect("mark without a reply", s.unreadTime(row("b")), 30);
  expect("opening a marked conversation reads its mark", s.ackTime(row("b")), 30);
  expect("the mark shows", s.unreadMark("b"), 30);
  s.read({ conversation_id: "b", read_at: 30 });
  expect("reading the mark clears it", s.unreadTime(row("b")), 0);
  expect("a cleared mark doesn't show", s.unreadMark("b"), 0);

  // Marking the open conversation holds it unread, replies included.
  s.mark({ conversation_id: "b", unread_at: 40 }, "b");
  expect("held mark is unread", s.unreadTime(row("b")), 40);
  expect("held mark is not acknowledged", s.ackTime(row("b")), 0);
  expect("held conversation's replies are not acknowledged", s.ackTime(row("b", 50)), 0);
  expect("held conversation's replies are unread", s.unreadTime(row("b", 50)), 50);
  s.snapshot(
    { first_seen_at: 10, receipts: [], unread_marks: [{ conversation_id: "b", unread_at: 40 }] },
    "b",
  );
  expect("a reconnect keeps the hold", s.ackTime(row("b", 50)), 0);
  s.release();
  expect("opening it again reads replies and mark", s.ackTime(row("b", 50)), 50);
  expect(
    "a receipt short of the mark",
    (s.read({ conversation_id: "b", read_at: 35 }), s.unreadTime(row("b"))),
    40,
  );
  s.read({ conversation_id: "b", read_at: 50 });
  expect("a receipt past the mark clears it", s.unreadTime(row("b", 50)), 0);

  // Marks elsewhere, and stale ones, hold nothing.
  s.mark({ conversation_id: "a", unread_at: 60 }, "b");
  expect("mark on another conversation", s.unreadTime(row("a", 25)), 60);
  expect("mark on another conversation is acknowledged once open", s.ackTime(row("a", 25)), 60);
  s.mark({ conversation_id: "b", unread_at: 45 }, "b");
  expect("a mark a receipt passed holds nothing", s.ackTime(row("b", 70)), 70);
  s.mark({ conversation_id: "a", unread_at: 55 }, "a");
  expect("an older mark doesn't hold", s.ackTime(row("a")), 60);

  // A reconnect brings a mark set meanwhile on the open conversation.
  s.snapshot(
    { first_seen_at: 10, receipts: null, unread_marks: [{ conversation_id: "c", unread_at: 80 }] },
    "c",
  );
  expect("a mark missed while disconnected holds", s.ackTime(row("c")), 0);
  expect("and is unread", s.unreadTime(row("c")), 80);
}

{
  // A reconnect replays the list before its snapshot, which may bring a mark
  // set meanwhile on the open conversation: acknowledge nothing until then.
  const s = new ReadMarks();
  s.snapshot({ first_seen_at: 10, receipts: [], unread_marks: [] }, "a");
  expect("the open conversation's reply is acknowledged", s.ackTime(row("a", 20)), 20);
  s.reconnected();
  expect("a replayed reply waits for the snapshot", s.ackTime(row("a", 40)), 0);
  expect("but is unread meanwhile", s.unreadTime(row("a", 40)), 40);
  s.snapshot(
    { first_seen_at: 10, receipts: [], unread_marks: [{ conversation_id: "a", unread_at: 30 }] },
    "a",
  );
  expect("the snapshot's mark holds it", s.ackTime(row("a", 40)), 0);
  s.reconnected();
  s.snapshot(
    { first_seen_at: 10, receipts: [], unread_marks: [{ conversation_id: "a", unread_at: 30 }] },
    "a",
  );
  s.release();
  expect("after the snapshot, replies are acknowledged", s.ackTime(row("a", 40)), 40);
}

{
  // Another device reading through the mark ends the hold.
  const s = new ReadMarks();
  s.snapshot({ first_seen_at: 10, receipts: [], unread_marks: [] }, "a");
  s.mark({ conversation_id: "a", unread_at: 30 }, "a");
  s.read({ conversation_id: "a", read_at: 25 });
  expect("an earlier receipt keeps the hold", s.ackTime(row("a", 40)), 0);
  s.read({ conversation_id: "a", read_at: 30 });
  expect("a receipt reaching the mark ends the hold", s.ackTime(row("a", 40)), 40);
}

{
  // Nothing is acknowledged while this client's mark is on its way, and a
  // first snapshot bringing it holds the open conversation.
  const s = new ReadMarks();
  s.marking.add("a");
  s.snapshot(
    { first_seen_at: 10, receipts: [], unread_marks: [{ conversation_id: "a", unread_at: 30 }] },
    "a",
  );
  expect("a pending mark blocks receipts", s.ackTime(row("a", 20)), 0);
  s.mark({ conversation_id: "a", unread_at: 30 }, "a");
  s.marking.delete("a");
  expect("the snapshot's mark of this client's holds", s.ackTime(row("a", 40)), 0);
}

{
  // Opening the conversation while the response is on its way reads it.
  const s = new ReadMarks();
  s.snapshot({ first_seen_at: 10, receipts: [], unread_marks: [] }, "b");
  s.marking.add("a");
  s.mark({ conversation_id: "a", unread_at: 30 }, "b");
  s.release();
  s.mark({ conversation_id: "a", unread_at: 30 }, "a");
  s.marking.delete("a");
  expect("the late response doesn't hold it", s.ackTime(row("a", 20)), 30);
}

if (failed > 0) process.exit(1);
console.log(`readMarks: ${passed} passed`);
