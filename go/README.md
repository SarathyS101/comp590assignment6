# Sleeping Barber - Go Implementation

## How to Run

```bash
cd go
go run main.go
```

## Design Notes

- Uses goroutines for each entity (shop owner, barber, waiting room, customers)
- Communication via typed messages on buffered channels
- Customer reply channels are buffered (size 1) to prevent goroutine leaks
- Barber has two receive states: main loop (actively serving) and sleep loop (idle)
- Waiting room manages queue state and barber sleep/wake transitions
- Satisfaction formula: `clamp(1, 5, 5 - floor(wait_sec / 3.0) + rand(-1,0,1))`
