# Sleeping Barber - Go Implementation

## How to Run

```bash
cd go
go run main.go
```

## Design Notes

- Uses goroutines for each entity (shop owner, barber, waiting room, customers)
- Communication exclusively via channels — no mutexes or sync primitives
- Barber and Waiting Room each use **multiple dedicated channels** with `select` to multiplex incoming messages (e.g., `wakeupCh`, `customerReadyCh`, `shutdownCh`)
- Barber tracks awake/sleeping state via a boolean (`isSleeping`) inside a single `for` loop
- Customer reply channels are buffered (size 1) to prevent goroutine leaks
- Main goroutine uses a `doneCh` channel to wait for all 20 customers to finish before collecting stats and shutting down
- `MsgNextCustomer` includes the barber's channel reference as payload (barber identity for wakeup)
- Satisfaction formula: `clamp(1, 5, 5 - floor(wait_sec / 3.0) + rand(-1,0,1))`
