# Sleeping Barber - Elixir Implementation

## How to Run

```bash
cd elixir
elixir sleeping_barber.exs
```

## Design Notes

- Uses Elixir processes (lightweight actors) for each entity
- Communication via tagged tuples and process mailboxes
- State managed through recursive function parameters (no mutable state)
- Pattern matching in `receive` blocks replaces switch statements
- `self()` provides process identity, replacing Go's explicit reply channels
- Barber has two recursive states: `main_loop` and `sleep_loop`
- Satisfaction formula: `clamp(1, 5, 5 - trunc(wait_sec / 3.0) + rand(-1,0,1))`
