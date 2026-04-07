# Reflection: Sleeping Barber Problem — Elixir vs Go
- Sarathy Selvam (PID: 730770538)
- Lakshin Ganesha (PID: 730757493)
- Sushant Potu (PID: 730768373)
## 1. Process Identity: PIDs vs Channels

In Elixir, every concurrent entity is a **process with a unique PID**. The barber, waiting room, and each customer are all spawned via `spawn/1`, and their PIDs serve as their addresses. When a customer arrives, it passes `self()` as part of the message (`{:arrive, self(), id, arrival_time}`), and the waiting room stores that PID to later send replies directly back to the customer. Identity and communication address are the same thing — knowing a process's PID is both knowing *who* it is and *how to reach it*.

In Go, goroutines are **anonymous** — they have no built-in identity or address. Instead, identity is expressed through **channels**. Each customer creates its own buffered reply channel (`replyCh := make(chan Message, 1)`) and passes it inside the `Message` struct's `From` field. The waiting room stores this channel in a `QueueEntry` so it can later send `MsgAdmitted`, `MsgTurnedAway`, or forward the channel to the barber for the rating handshake. The barber similarly uses a dedicated `ratingCh` to receive ratings without conflicting with its other incoming channels. The barber and waiting room each have **multiple dedicated channels** (e.g., `wakeupCh`, `customerReadyCh`, `shutdownCh` for the barber; `arriveCh`, `nextCustomerCh`, `shutdownCh` for the waiting room), and use `select` to multiplex across them.

This difference had a concrete impact during development: Go required careful reasoning about channel topology — which goroutine owns which channel, and ensuring that messages are routed to the correct dedicated channel. An early version used a single channel per goroutine, which caused a deadlock when the barber's `doHaircut` function read a `MsgGetStats` message instead of the expected `MsgRating`. Splitting into dedicated channels resolved this. In Elixir, selective `receive` with pattern matching (`{:rating, ^customer_id, score}`) naturally ignores irrelevant messages in the mailbox, making this class of bug impossible.

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

The Elixir approach makes state transitions visible in the code structure — you can see exactly what state is carried into each mode. The Go approach is more concise but makes it less obvious which variables are live across state transitions, since both branches of the `if isSleeping` check share the same scope.

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

Go's channel-based approach uses **`select`** across multiple dedicated channels. The barber has separate channels for wakeups, customer-ready notifications, none-waiting signals, stats requests, and shutdown. A boolean `isSleeping` controls which `select` block runs:

```go
for {
    if isSleeping {
        select {
        case msg := <-wakeupCh: ...
        case msg := <-statsCh: ...
        case <-shutdownCh: ...
        }
    } else {
        select {
        case msg := <-customerReadyCh: ...
        case <-noneWaitingCh: ...
        case msg := <-statsCh: ...
        case <-shutdownCh: ...
        }
    }
}
```

The `select` statement blocks until one of the channels is ready, providing natural multiplexing. Each state (sleeping vs. awake) listens on a different set of channels, so the barber only receives messages appropriate to its current mode. This avoids the problem of misrouted messages — a wakeup can only arrive on `wakeupCh`, and a customer-ready notification only on `customerReadyCh`.

Elixir's `receive` block is fundamentally different: it performs **selective receive** against the process mailbox. Only messages matching a pattern are consumed; non-matching messages remain in the mailbox for later:

```elixir
receive do
  {:rating, ^customer_id, score} -> ...
end
```

The `^customer_id` pin operator ensures only the rating from the specific customer being served is consumed. If a `{:get_stats, from_pid}` message arrives in the barber's mailbox during a haircut, it stays there untouched until the barber transitions to a state with a `receive` clause that matches it. This is why the Elixir implementation never had the channel-mixing deadlock — selective receive naturally queues unexpected messages.

The trade-off is performance: Elixir's selective receive scans the entire mailbox for each `receive`, which can be O(n) for large mailboxes. Go's `select` over dedicated channels is O(1) but requires the programmer to design the channel topology upfront and manually route every message to the correct channel.

## 6. AI Tool Usage

I did **not** use Claude Code or any AI coding assistant for the Go portion of this assignment. The Go implementation was written entirely by me, including the algorithm design, message protocol, multi-channel `select` architecture, goroutine structure, and debugging (such as the deadlock bug described in section 1, which I diagnosed and fixed by introducing dedicated channels per message type). For the Go code, I only used AI tools in ways consistent with the course AI policy — for looking up specific syntax details and language usage questions, not for generating solutions or roughing out overall code structure.

For the **Elixir portion**, I used Claude Code (Claude AI) to assist with the implementation, translating the architecture and design I had already developed in Go into Elixir's process-based concurrency model.

**What worked well with AI on the Elixir side:**
- Translating the established Go architecture into Elixir — the structural mapping (goroutines ↔ processes, channels ↔ mailboxes, select ↔ receive) was handled correctly
- Producing an identical logging format to the Go implementation, making runtime comparison straightforward
- Getting the core concurrency logic right: the sleep/wake handshake, queue management, and customer lifecycle

**What required intervention on the Elixir side:**
- The Elixir implementation had an **unused variable warning** (`wr_pid` in `do_haircut`) that needed a simple prefix fix to `_wr_pid`
- The `next_customer` message needed to include `self()` (the barber's PID) as payload to match the message protocol specification: `{:next_customer, self()}` instead of just `{:next_customer}`
- A timing edge case where the last customer being served during stats collection isn't counted in the closing report (e.g., Elixir reported 13 served + 5 turned away = 18 out of 20). This is inherent to the grace-period design, not a bug

---


