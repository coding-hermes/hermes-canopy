# Iteration side panel

The iteration side panel is the Canopy view for following active work from an
agent. It is available from the main application header.

## Open and close the panel

Select **Agent activity** in the header to open the panel. The panel is not
opened automatically just because an iteration is active. It opens on the
right side of the application and loads the currently active iteration cards.

Select the close button in the panel, or press **Escape**, to close it. Closing
the panel stops its live event streams; opening it again loads the active cards
and their current progress again.

## What the panel shows

The panel header is **Agent activity**. Below it, a live summary shows the
current progress segments. The card list contains active iteration cards. If
there are no active cards, the panel says **No active agent cards**.

Each card can show:

- its title and activity type: **Search**, **Code Exec**, **File Read**,
  **Thinking**, or **Tool Call**;
- its current state, such as **Running**, **Waiting**, **Done**, **Failed**,
  **Cancelled**, or **Interrupted**;
- a concise progress value appropriate to the activity:
  - Search: the number of retrieved results;
  - Code Exec: whether it is running and cancellable, its state, or its exit
    code;
  - File Read: the file path and visible line range;
  - Thinking: the active step, or the current and total step counts;
  - Tool Call: an approval prompt such as **Approve [tool]**, or its status;
- the time of the **Last interaction**.

Select **Expand** on a card to see its **Activity** feed. The feed lists
committed activity event types and their sequence numbers. Before any activity
has arrived, it says **No committed activity received yet**. The expanded view
also includes the card's current details in a scrollable data block.

An interrupted card displays a recovery notice: recovery is required after an
interrupted agent process, and prior activity is preserved.

For a running **Code Exec** card, **Cancel** is available. **Dismiss** is
available on each card. Dismissing is separate from cancelling an execution.

## How updates arrive

When the panel opens, Canopy loads the active iteration cards and their current
progress, then opens a live server-sent events (SSE) stream for each card:

`GET /api/v1/cards/iteration/{card_id}/events`

New committed iteration events update the card and add an entry to its Activity
feed. A card snapshot can refresh the card's displayed state. The service also
sends heartbeat frames every 30 seconds to keep the connection known to be
alive; heartbeats are not shown as activity entries.

If a stream reconnects, the service can replay committed frames from the
stream cursor so the panel can catch up with activity received while the
connection was unavailable. The complete endpoint and replay details are in
[API.md — Iteration Cards (SPEC-PL-04 phase 2)](API.md#iteration-cards-spec-pl-04-phase-2).

## Dismissing a card

Select **Dismiss** when you no longer want an active card in the panel. The
panel requests a dismissal of the card. It keeps the card visible until the
server sends the committed `card_dismissed` frame; then it closes that card's
stream and removes the card from the active list. If the stream is already
closed, the panel removes the card after the dismissal request succeeds.

The dismissal frame is durable: it is forwarded only after the base-card
lifecycle row has committed. It is also one-shot. After the first dismissal
frame, no later base-card lifecycle events are forwarded on that stream.

## Where to look next

For creating, reading, updating, cancelling, listing, and streaming iteration
cards, see [API.md — Iteration Cards (SPEC-PL-04 phase 2)](API.md#iteration-cards-spec-pl-04-phase-2).
