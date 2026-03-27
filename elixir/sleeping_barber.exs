# ── Config ──────────────────────────────────────────────────────────────────

config = %{
  total_customers: 20,
  wr_capacity: 5,
  arrival_min_ms: 500,
  arrival_max_ms: 2000,
  haircut_min_ms: 1000,
  haircut_max_ms: 4000,
  threshold_sec: 3.0,
  grace_ms: 8000
}

# ── Logging ─────────────────────────────────────────────────────────────────

defmodule Log do
  def event(start_time, entity, detail) do
    elapsed_ms = System.monotonic_time(:millisecond) - start_time
    h = rem(div(elapsed_ms, 3_600_000), 24)
    m = rem(div(elapsed_ms, 60_000), 60)
    s = rem(div(elapsed_ms, 1000), 60)
    ms = rem(elapsed_ms, 1000)
    entity_padded = String.pad_trailing(entity, 12)
    IO.puts("[#{pad(h)}:#{pad(m)}:#{pad(s)}.#{pad3(ms)}] [#{entity_padded}] #{detail}")
  end

  defp pad(n), do: n |> Integer.to_string() |> String.pad_leading(2, "0")
  defp pad3(n), do: n |> Integer.to_string() |> String.pad_leading(3, "0")
end

# ── Waiting Room ────────────────────────────────────────────────────────────

defmodule WaitingRoom do
  def start(barber_pid, config, start_time) do
    spawn(fn -> loop([], config.wr_capacity, barber_pid, true, 0, config, start_time) end)
  end

  defp loop(queue, capacity, barber_pid, barber_sleeping, turned_away, config, start_time) do
    receive do
      {:arrive, customer_pid, id, arrival_time} ->
        if length(queue) >= capacity do
          Log.event(start_time, "WaitingRoom", "Customer #{id} turned away (queue full: #{length(queue)}/#{capacity})")
          send(customer_pid, {:turned_away})
          loop(queue, capacity, barber_pid, barber_sleeping, turned_away + 1, config, start_time)
        else
          new_queue = queue ++ [{customer_pid, id, arrival_time}]
          Log.event(start_time, "WaitingRoom", "Customer #{id} admitted (queue: #{length(new_queue)}/#{capacity})")
          send(customer_pid, {:admitted})

          if barber_sleeping and length(new_queue) > 0 do
            [{front_pid, front_id, front_arrival} | rest] = new_queue
            send(barber_pid, {:wakeup, front_pid, front_id, front_arrival})
            loop(rest, capacity, barber_pid, false, turned_away, config, start_time)
          else
            loop(new_queue, capacity, barber_pid, barber_sleeping, turned_away, config, start_time)
          end
        end

      {:next_customer} ->
        if length(queue) > 0 do
          [{front_pid, front_id, front_arrival} | rest] = queue
          send(barber_pid, {:customer_ready, front_pid, front_id, front_arrival})
          loop(rest, capacity, barber_pid, barber_sleeping, turned_away, config, start_time)
        else
          send(barber_pid, {:none_waiting})
          loop(queue, capacity, barber_pid, true, turned_away, config, start_time)
        end

      {:get_stats, from_pid} ->
        send(from_pid, {:wr_stats_reply, turned_away})
        loop(queue, capacity, barber_pid, barber_sleeping, turned_away, config, start_time)

      {:shutdown} ->
        Log.event(start_time, "WaitingRoom", "Shutting down")
    end
  end
end

# ── Barber ──────────────────────────────────────────────────────────────────

defmodule Barber do
  def start(config, start_time) do
    spawn(fn ->
      Log.event(start_time, "Barber", "Spawned, going to sleep (no customers)")
      sleep_loop(nil, 0, 0, 0, config, start_time)
    end)
  end

  def set_wr(barber_pid, wr_pid) do
    send(barber_pid, {:set_wr, wr_pid})
  end

  defp do_haircut(customer_pid, customer_id, arrival_time, _wr_pid, served, total_dur, total_rating, config, start_time) do
    cut_ms = config.haircut_min_ms + :rand.uniform(config.haircut_max_ms - config.haircut_min_ms + 1) - 1
    now_ms = System.monotonic_time(:millisecond) - start_time
    wait_ms = now_ms - arrival_time
    Log.event(start_time, "Barber", "Cutting hair for Customer #{customer_id} (duration: #{cut_ms}ms, waited: #{wait_ms}ms)")

    Process.sleep(cut_ms)

    Log.event(start_time, "Barber", "Finished cutting Customer #{customer_id}'s hair")

    send(customer_pid, {:rate_request, self()})

    receive do
      {:rating, ^customer_id, score} ->
        new_served = served + 1
        new_total_dur = total_dur + cut_ms + wait_ms
        new_total_rating = total_rating + score
        avg_dur = new_total_dur / new_served
        avg_rat = new_total_rating / new_served
        Log.event(start_time, "Barber", "Customer #{customer_id} rated: #{score}/5 | Running avg: #{Float.round(avg_rat / 1, 1)}/5, avg duration: #{avg_dur}ms (#{new_served} served)")
        {new_served, new_total_dur, new_total_rating}
    end
  end

  defp main_loop(wr_pid, served, total_dur, total_rating, config, start_time) do
    send(wr_pid, {:next_customer})

    receive do
      {:customer_ready, customer_pid, customer_id, arrival_time} ->
        {new_served, new_total_dur, new_total_rating} =
          do_haircut(customer_pid, customer_id, arrival_time, wr_pid, served, total_dur, total_rating, config, start_time)
        main_loop(wr_pid, new_served, new_total_dur, new_total_rating, config, start_time)

      {:none_waiting} ->
        Log.event(start_time, "Barber", "No customers waiting, going to sleep")
        sleep_loop(wr_pid, served, total_dur, total_rating, config, start_time)

      {:get_stats, from_pid} ->
        send_stats(from_pid, served, total_dur, total_rating)
        main_loop(wr_pid, served, total_dur, total_rating, config, start_time)

      {:shutdown} ->
        Log.event(start_time, "Barber", "Shutting down")
    end
  end

  defp sleep_loop(wr_pid, served, total_dur, total_rating, config, start_time) do
    receive do
      {:set_wr, new_wr_pid} ->
        sleep_loop(new_wr_pid, served, total_dur, total_rating, config, start_time)

      {:wakeup, customer_pid, customer_id, arrival_time} ->
        Log.event(start_time, "Barber", "Woken up by Customer #{customer_id}")
        {new_served, new_total_dur, new_total_rating} =
          do_haircut(customer_pid, customer_id, arrival_time, wr_pid, served, total_dur, total_rating, config, start_time)
        main_loop(wr_pid, new_served, new_total_dur, new_total_rating, config, start_time)

      {:get_stats, from_pid} ->
        send_stats(from_pid, served, total_dur, total_rating)
        sleep_loop(wr_pid, served, total_dur, total_rating, config, start_time)

      {:shutdown} ->
        Log.event(start_time, "Barber", "Shutting down")
    end
  end

  defp send_stats(from_pid, served, total_dur, total_rating) do
    avg_dur = if served > 0, do: total_dur / served, else: 0
    avg_rat = if served > 0, do: total_rating / served, else: 0.0
    send(from_pid, {:barber_stats_reply, served, avg_dur, avg_rat})
  end
end

# ── Customer ────────────────────────────────────────────────────────────────

defmodule Customer do
  def start(id, wr_pid, config, start_time) do
    spawn(fn -> run(id, wr_pid, config, start_time) end)
  end

  defp run(id, wr_pid, config, start_time) do
    arrival_time = System.monotonic_time(:millisecond) - start_time
    Log.event(start_time, "Customer", "Customer #{id} arrives")

    send(wr_pid, {:arrive, self(), id, arrival_time})

    receive do
      {:turned_away} ->
        Log.event(start_time, "Customer", "Customer #{id} leaves (turned away)")

      {:admitted} ->
        receive do
          {:rate_request, barber_pid} ->
            now_ms = System.monotonic_time(:millisecond) - start_time
            wait_sec = (now_ms - arrival_time) / 1000.0
            score = 5 - trunc(wait_sec / config.threshold_sec) + (:rand.uniform(3) - 2)
            score = max(1, min(5, score))
            Log.event(start_time, "Customer", "Customer #{id} gives rating: #{score}/5 (wait: #{Float.round(wait_sec, 1)}s)")
            send(barber_pid, {:rating, id, score})
        end
    end
  end
end

# ── Shop Owner (main) ──────────────────────────────────────────────────────

start_time = System.monotonic_time(:millisecond)

Log.event(start_time, "Shop", "=== Sleeping Barber Shop Opens ===")

barber_pid = Barber.start(config, start_time)
wr_pid = WaitingRoom.start(barber_pid, config, start_time)
Barber.set_wr(barber_pid, wr_pid)

for i <- 1..config.total_customers do
  Customer.start(i, wr_pid, config, start_time)
  sleep_ms = config.arrival_min_ms + :rand.uniform(config.arrival_max_ms - config.arrival_min_ms + 1) - 1
  Log.event(start_time, "Shop", "Next customer in #{sleep_ms}ms")
  Process.sleep(sleep_ms)
end

Log.event(start_time, "Shop", "All #{config.total_customers} customers sent. Grace period: #{config.grace_ms}ms")
Process.sleep(config.grace_ms)

# Collect stats
send(barber_pid, {:get_stats, self()})
barber_stats =
  receive do
    {:barber_stats_reply, served, avg_dur, avg_rat} -> %{served: served, avg_duration: avg_dur, avg_rating: avg_rat}
  end

send(wr_pid, {:get_stats, self()})
wr_stats =
  receive do
    {:wr_stats_reply, turned_away} -> %{turned_away: turned_away}
  end

# Shutdown
send(barber_pid, {:shutdown})
Process.sleep(100)
send(wr_pid, {:shutdown})
Process.sleep(100)

# Closing report
Log.event(start_time, "Shop", "=== Closing Report ===")
Log.event(start_time, "Shop", "Customers served:     #{barber_stats.served}")
Log.event(start_time, "Shop", "Customers turned away: #{wr_stats.turned_away}")
Log.event(start_time, "Shop", "Total customers:      #{barber_stats.served + wr_stats.turned_away}")
Log.event(start_time, "Shop", "Avg service duration: #{barber_stats.avg_duration}ms")
Log.event(start_time, "Shop", "Avg satisfaction:     #{Float.round(barber_stats.avg_rating / 1, 1)}/5")
Log.event(start_time, "Shop", String.duplicate("─", 40))
