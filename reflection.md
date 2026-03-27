# Reflection: Sleeping Barber Problem — Elixir vs Go
- Sarathy Selvam (PID: 730770538)
## 1. Process Identity: PIDs vs Channels

In Elixir, every concurrent entity is a **process with a unique PID**. The barber, waiting room, and each customer are all spawned via `spawn/1`, and their PIDs serve as their addresses. When a customer arrives, it passes `self()` as part of the message (`{:arrive, self(), id, arrival_time}`), and the waiting room stores that PID to later send replies directly back to the customer. Identity and communication address are the same thing — knowing a process's PID is both knowing *who* it is and *how to reach it*.

In Go, goroutines are **anonymous** — they have no built-in identity or address. Instead, identity is expressed through **channels**. Each customer creates its own buffered reply channel (`replyCh := make(chan Message, 1)`) and passes it inside the `Message` struct's `From` field. The waiting room stores this channel in a `QueueEntry` so it can later send `MsgAdmitted`, `MsgTurnedAway`, or forward the channel to the barber for the rating handshake. The barber similarly uses a dedicated `ratingCh` to receive ratings without conflicting with its main message channel.

This difference had a concrete impact during development: Go required careful reasoning about which channel to read from at each point (the barber's main channel vs. the dedicated rating channel), and an early bug caused a deadlock when the barber's `doHaircut` function read a `MsgGetStats` message from its main channel instead of the expected `MsgRating`. In Elixir, selective `receive` with pattern matching (`{:rating, ^customer_id, score}`) naturally ignores irrelevant messages in the mailbox, making this class of bug impossible.

## 2. State Management: Recursive Parameters vs Local Variables

In Go, the barber goroutine maintains state via **local variables** declared at the top of the function:

```go
served := 0
totalDurMs := int64(0)
totalRating := 0
```

These variables are mutated in place by `doHaircut` (a closure that captures them) using `served++`, `totalRating += rating.Value`, etc. The waiting room similarly mutates its `queue` slice and `turnedAway` counter. This is straightforward imperative programming — state lives in mutable variables scoped to the goroutine.

In Elixir, state is managed through **recursive function parameters**. The barber's `main_loop` carries `served`, `total_dur`, and `total_rating` as arguments, and each recursive call passes updated values:

```elixir
defp main_loop(wr_pid, served, total_dur, total_rating, config, start_time) do
  ...
  main_loop(wr_pid, new_served, new_total_dur, new_total_rating, config, start_time)
end
```

The `do_haircut` function returns a tuple `{new_served, new_total_dur, new_total_rating}` rather than mutating anything. State transitions between the barber's two modes (`main_loop` ↔ `sleep_loop`) carry all state in the function call, making each state transition explicit. The waiting room similarly threads `queue`, `turned_away`, and `barber_sleeping` through every recursive `loop` call.

The Elixir approach makes state transitions visible in the code structure — you can see exactly what state is carried into each mode. The Go approach is more concise but makes it less obvious which variables are live across state transitions, since the `goto` labels share the same scope.

## 3. The Sleeping Barber Handshake

Both implementations follow the same message flow, visible in the runtime logs. When Customer 1 arrives to find a sleeping barber:

**Go output:**
```
[00:00:00.000] [Customer    ] Customer 1 arrives
[00:00:00.000] [WaitingRoom ] Customer 1 admitted (queue: 1/5)
[00:00:00.000] [Barber      ] Woken up by Customer 1
[00:00:00.000] [Barber      ] Cutting hair for Customer 1 (duration: 2228ms, waited: 0ms)
```

**Elixir output:**
```
[00:00:00.001] [Customer    ] Customer 1 arrives
[00:00:00.001] [WaitingRoom ] Customer 1 admitted (queue: 1/5)
[00:00:00.001] [Barber      ] Woken up by Customer 1
[00:00:00.001] [Barber      ] Cutting hair for Customer 1 (duration: 3346ms, waited: 1ms)
```

The handshake involves three entities and five messages:

1. **Customer → Waiting Room**: `MsgArrive` / `{:arrive, pid, id, time}` — customer announces arrival
2. **Waiting Room → Customer**: `MsgAdmitted` / `{:admitted}` — customer is added to queue
3. **Waiting Room → Barber**: `MsgWakeup` / `{:wakeup, pid, id, time}` — WR detects `barberSleeping == true`, dequeues the front customer, and sends a wakeup message containing the customer's identity (reply channel in Go, PID in Elixir)
4. **Barber → Customer**: `MsgRateRequest` / `{:rate_request, barber_pid}` — after finishing the haircut
5. **Customer → Barber**: `MsgRating` / `{:rating, id, score}` — customer sends satisfaction score

The key design decision is that the **waiting room mediates** the sleep/wake transition. The barber never communicates directly with arriving customers — it only learns about them through the waiting room. This prevents race conditions: the WR atomically checks `barberSleeping`, dequeues, and sends the wakeup in a single message-handling step. In Go, the WR flips `barberSleeping = false` before sending `MsgWakeup`; in Elixir, the recursive call passes `false` for `barber_sleeping`.

## 4. Message Types: Pattern Matching vs Structs

Go uses a **single `Message` struct** with a `Kind` field (an `iota` enum of 12 constants) and a union of payload fields (`From`, `CustomerID`, `Value`, `ArrivalMs`, stats fields). Every message is the same type, and receivers dispatch on `msg.Kind` with a `switch` statement:

```go
switch msg.Kind {
case MsgCustomerReady:
    doHaircut(msg.From, msg.CustomerID, msg.ArrivalMs)
case MsgNoneWaiting:
    ...
}
```

This means every `Message` carries all fields even when most are unused — a `MsgShutdown` still has `CustomerID`, `ArrivalMs`, etc. (set to zero values). The compiler provides no help ensuring the right fields are populated for each message kind.

Elixir uses **tagged tuples** where each message type has its own shape:

```elixir
{:arrive, customer_pid, id, arrival_time}
{:admitted}
{:shutdown}
{:rating, customer_id, score}
```

The `receive` block uses **pattern matching** to both dispatch and destructure in one step:

```elixir
receive do
  {:customer_ready, customer_pid, customer_id, arrival_time} -> ...
  {:none_waiting} -> ...
end
```

The trade-off: Go's approach is more explicit and refactoring-friendly (rename a struct field and the compiler catches all uses), but verbose and error-prone (nothing prevents reading `msg.CustomerID` from a `MsgShutdown`). Elixir's approach is more concise and self-documenting (each message's shape is its type), but lacks compile-time checking — a misspelled atom like `:shutdwon` would silently fail to match.

## 5. Select vs Receive

Go's channel-based approach uses **`<-ch` in a `for` loop** with a `switch` on the message kind. The barber reads from a single channel and dispatches:

```go
for {
    msg := <-ch
    switch msg.Kind {
    case MsgWakeup: ...
    case MsgGetStats: ...
    case MsgShutdown: ...
    }
}
```

Go does have a `select` statement for reading from multiple channels, but in this implementation the barber uses a single channel for all incoming messages. The distinction between "main loop" and "sleep loop" is achieved via `goto` labels — both read from the same channel, but accept different message kinds. Messages that don't match any `case` in the `switch` are silently dropped, which caused the deadlock bug described in Question 1.

Elixir's `receive` block is fundamentally different: it performs **selective receive** against the process mailbox. Only messages matching a pattern are consumed; non-matching messages remain in the mailbox for later:

```elixir
receive do
  {:rating, ^customer_id, score} -> ...
end
```

The `^customer_id` pin operator ensures only the rating from the specific customer being served is consumed. If a `{:get_stats, from_pid}` message arrives in the barber's mailbox during a haircut, it stays there untouched until the barber transitions to a state with a `receive` clause that matches it. This is why the Elixir implementation never had the channel-mixing deadlock — selective receive naturally queues unexpected messages.

The trade-off is performance: Elixir's selective receive scans the entire mailbox for each `receive`, which can be O(n) for large mailboxes. Go's channel read is O(1) but requires the programmer to manually handle or route every message that arrives.

## 6. AI Tool Usage

Claude Code (Claude AI) was used to generate both implementations from a detailed architectural plan. The plan specified the message types, goroutine/process structure, state management approach, and satisfaction formula.

**What worked well:**
- Translating the plan into working code for both languages simultaneously — the structural mapping (goroutines ↔ processes, channels ↔ mailboxes, switch ↔ receive) was handled correctly
- Producing identical logging formats across both implementations, making runtime comparison straightforward
- Getting the core concurrency logic right: the sleep/wake handshake, queue management, and customer lifecycle all worked on the first run

**What required intervention:**
- The Go implementation had a **deadlock bug** where the barber's `doHaircut` function received from the barber's main channel (`<-ch`) for the rating response, but could accidentally consume a `MsgGetStats` message sent by the shop owner during the grace period. This was detected by running the program to completion and observing the `fatal error: all goroutines are asleep - deadlock!` panic. The fix was to introduce a dedicated `ratingCh` channel so the rating receive couldn't interfere with the barber's main message channel
- The Elixir implementation had an **unused variable warning** (`wr_pid` in `do_haircut`) that needed a simple prefix fix to `_wr_pid`
- Both implementations exhibit a timing edge case where the last customer being served during stats collection isn't counted in the closing report (e.g., Go reported 12 served + 7 turned away = 19 out of 20; Elixir reported 13 served + 5 turned away = 18 out of 20). This is inherent to the grace-period design, not a bug

---


