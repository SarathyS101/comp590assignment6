package main

import (
	"fmt"
	"math/rand"
	"strings"
	"sync"
	"time"
)

// ── Config ──────────────────────────────────────────────────────────────────

type Config struct {
	TotalCustomers int
	WRCapacity     int
	ArrivalMinMs   int
	ArrivalMaxMs   int
	HaircutMinMs   int
	HaircutMaxMs   int
	ThresholdSec   float64
	GraceMs        int
}

func defaultConfig() Config {
	return Config{
		TotalCustomers: 20,
		WRCapacity:     5,
		ArrivalMinMs:   500,
		ArrivalMaxMs:   2000,
		HaircutMinMs:   1000,
		HaircutMaxMs:   4000,
		ThresholdSec:   3.0,
		GraceMs:        8000,
	}
}

// ── Message types ───────────────────────────────────────────────────────────

type MsgKind int

const (
	MsgArrive MsgKind = iota
	MsgAdmitted
	MsgTurnedAway
	MsgNextCustomer
	MsgCustomerReady
	MsgNoneWaiting
	MsgWakeup
	MsgRateRequest
	MsgRating
	MsgGetStats
	MsgStatsReply
	MsgShutdown
)

type Message struct {
	Kind       MsgKind
	From       chan Message
	CustomerID int
	Value      int
	ArrivalMs  int64

	// Stats fields
	Served      int
	TurnedAway  int
	AvgDuration float64
	AvgRating   float64
}

// ── Logging ─────────────────────────────────────────────────────────────────

var startTime = time.Now()

func logEvent(entity, detail string) {
	elapsed := time.Since(startTime)
	h := int(elapsed.Hours()) % 24
	m := int(elapsed.Minutes()) % 60
	s := int(elapsed.Seconds()) % 60
	ms := int(elapsed.Milliseconds()) % 1000
	fmt.Printf("[%02d:%02d:%02d.%03d] [%-12s] %s\n", h, m, s, ms, entity, detail)
}

// ── Queue entry ─────────────────────────────────────────────────────────────

type QueueEntry struct {
	CustomerID int
	ReplyCh    chan Message
	ArrivalMs  int64
}

// ── Waiting Room ────────────────────────────────────────────────────────────

func waitingRoom(ch chan Message, barberCh chan Message, cfg Config) {
	queue := make([]QueueEntry, 0, cfg.WRCapacity)
	barberSleeping := true
	turnedAway := 0

	for {
		msg := <-ch
		switch msg.Kind {

		case MsgArrive:
			if len(queue) >= cfg.WRCapacity {
				turnedAway++
				logEvent("WaitingRoom", fmt.Sprintf("Customer %d turned away (queue full: %d/%d)", msg.CustomerID, len(queue), cfg.WRCapacity))
				msg.From <- Message{Kind: MsgTurnedAway}
			} else {
				entry := QueueEntry{
					CustomerID: msg.CustomerID,
					ReplyCh:    msg.From,
					ArrivalMs:  msg.ArrivalMs,
				}
				queue = append(queue, entry)
				logEvent("WaitingRoom", fmt.Sprintf("Customer %d admitted (queue: %d/%d)", msg.CustomerID, len(queue), cfg.WRCapacity))
				msg.From <- Message{Kind: MsgAdmitted}

				if barberSleeping && len(queue) > 0 {
					front := queue[0]
					queue = queue[1:]
					barberSleeping = false
					barberCh <- Message{
						Kind:       MsgWakeup,
						From:       front.ReplyCh,
						CustomerID: front.CustomerID,
						ArrivalMs:  front.ArrivalMs,
					}
				}
			}

		case MsgNextCustomer:
			if len(queue) > 0 {
				front := queue[0]
				queue = queue[1:]
				barberCh <- Message{
					Kind:       MsgCustomerReady,
					From:       front.ReplyCh,
					CustomerID: front.CustomerID,
					ArrivalMs:  front.ArrivalMs,
				}
			} else {
				barberSleeping = true
				barberCh <- Message{Kind: MsgNoneWaiting}
			}

		case MsgGetStats:
			msg.From <- Message{
				Kind:       MsgStatsReply,
				TurnedAway: turnedAway,
			}

		case MsgShutdown:
			logEvent("WaitingRoom", "Shutting down")
			return
		}
	}
}

// ── Barber ───────────────────────────────────────────────────────────────────

func barber(ch chan Message, wrCh chan Message, cfg Config) {
	served := 0
	totalDurMs := int64(0)
	totalRating := 0

	ratingCh := make(chan Message, 1)

	doHaircut := func(customerCh chan Message, customerID int, arrivalMs int64) {
		cutMs := cfg.HaircutMinMs + rand.Intn(cfg.HaircutMaxMs-cfg.HaircutMinMs+1)
		waitMs := time.Since(startTime).Milliseconds() - arrivalMs
		logEvent("Barber", fmt.Sprintf("Cutting hair for Customer %d (duration: %dms, waited: %dms)", customerID, cutMs, waitMs))

		time.Sleep(time.Duration(cutMs) * time.Millisecond)

		totalDurMs += int64(cutMs) + waitMs

		logEvent("Barber", fmt.Sprintf("Finished cutting Customer %d's hair", customerID))

		// Request rating from customer — use dedicated ratingCh to avoid mixing with barber messages
		customerCh <- Message{Kind: MsgRateRequest, From: ratingCh}

		// Wait for rating on dedicated channel
		rating := <-ratingCh
		if rating.Kind == MsgRating {
			totalRating += rating.Value
			served++
			avgDur := float64(totalDurMs) / float64(served)
			avgRat := float64(totalRating) / float64(served)
			logEvent("Barber", fmt.Sprintf("Customer %d rated: %d/5 | Running avg: %.1f/5, avg duration: %.0fms (%d served)",
				customerID, rating.Value, avgRat, avgDur, served))
		}
	}

	handleStats := func(msg Message) {
		avgDur := 0.0
		avgRat := 0.0
		if served > 0 {
			avgDur = float64(totalDurMs) / float64(served)
			avgRat = float64(totalRating) / float64(served)
		}
		msg.From <- Message{
			Kind:        MsgStatsReply,
			Served:      served,
			AvgDuration: avgDur,
			AvgRating:   avgRat,
		}
	}

	logEvent("Barber", "Spawned, going to sleep (no customers)")

	// Start in sleep loop since no customers yet
	goto sleepLoop

mainLoop:
	for {
		wrCh <- Message{Kind: MsgNextCustomer}
		msg := <-ch
		switch msg.Kind {
		case MsgCustomerReady:
			doHaircut(msg.From, msg.CustomerID, msg.ArrivalMs)
		case MsgNoneWaiting:
			logEvent("Barber", "No customers waiting, going to sleep")
			goto sleepLoop
		case MsgGetStats:
			handleStats(msg)
			goto mainLoop
		case MsgShutdown:
			logEvent("Barber", "Shutting down")
			return
		}
	}

sleepLoop:
	for {
		msg := <-ch
		switch msg.Kind {
		case MsgWakeup:
			logEvent("Barber", fmt.Sprintf("Woken up by Customer %d", msg.CustomerID))
			doHaircut(msg.From, msg.CustomerID, msg.ArrivalMs)
			goto mainLoop
		case MsgGetStats:
			handleStats(msg)
		case MsgShutdown:
			logEvent("Barber", "Shutting down")
			return
		}
	}
}

// ── Customer ────────────────────────────────────────────────────────────────

func customer(id int, wrCh chan Message, cfg Config, wg *sync.WaitGroup) {
	defer wg.Done()

	replyCh := make(chan Message, 1)
	arrivalMs := time.Since(startTime).Milliseconds()

	logEvent("Customer", fmt.Sprintf("Customer %d arrives", id))

	wrCh <- Message{
		Kind:       MsgArrive,
		From:       replyCh,
		CustomerID: id,
		ArrivalMs:  arrivalMs,
	}

	reply := <-replyCh
	if reply.Kind == MsgTurnedAway {
		logEvent("Customer", fmt.Sprintf("Customer %d leaves (turned away)", id))
		return
	}

	// Admitted — wait for rate request
	rateReq := <-replyCh
	if rateReq.Kind == MsgRateRequest {
		waitSec := float64(time.Since(startTime).Milliseconds()-arrivalMs) / 1000.0
		score := 5 - int(waitSec/cfg.ThresholdSec) + (rand.Intn(3) - 1)
		if score < 1 {
			score = 1
		}
		if score > 5 {
			score = 5
		}
		logEvent("Customer", fmt.Sprintf("Customer %d gives rating: %d/5 (wait: %.1fs)", id, score, waitSec))
		rateReq.From <- Message{Kind: MsgRating, CustomerID: id, Value: score}
	}
}

// ── Shop Owner (main) ───────────────────────────────────────────────────────

func main() {
	cfg := defaultConfig()

	logEvent("Shop", "=== Sleeping Barber Shop Opens ===")

	wrCh := make(chan Message, 10)
	barberCh := make(chan Message, 10)

	go waitingRoom(wrCh, barberCh, cfg)
	go barber(barberCh, wrCh, cfg)

	var wg sync.WaitGroup

	for i := 1; i <= cfg.TotalCustomers; i++ {
		wg.Add(1)
		go customer(i, wrCh, cfg, &wg)
		sleepMs := cfg.ArrivalMinMs + rand.Intn(cfg.ArrivalMaxMs-cfg.ArrivalMinMs+1)
		logEvent("Shop", fmt.Sprintf("Next customer in %dms", sleepMs))
		time.Sleep(time.Duration(sleepMs) * time.Millisecond)
	}

	logEvent("Shop", fmt.Sprintf("All %d customers sent. Grace period: %dms", cfg.TotalCustomers, cfg.GraceMs))
	time.Sleep(time.Duration(cfg.GraceMs) * time.Millisecond)

	// Collect stats
	statsCh := make(chan Message, 1)

	barberCh <- Message{Kind: MsgGetStats, From: statsCh}
	barberStats := <-statsCh

	wrCh <- Message{Kind: MsgGetStats, From: statsCh}
	wrStats := <-statsCh

	// Shutdown
	barberCh <- Message{Kind: MsgShutdown}
	time.Sleep(100 * time.Millisecond)
	wrCh <- Message{Kind: MsgShutdown}
	time.Sleep(100 * time.Millisecond)

	// Wait for customers to finish
	wg.Wait()

	// Closing report
	logEvent("Shop", "=== Closing Report ===")
	logEvent("Shop", fmt.Sprintf("Customers served:     %d", barberStats.Served))
	logEvent("Shop", fmt.Sprintf("Customers turned away: %d", wrStats.TurnedAway))
	logEvent("Shop", fmt.Sprintf("Total customers:      %d", barberStats.Served+wrStats.TurnedAway))
	logEvent("Shop", fmt.Sprintf("Avg service duration: %.0fms", barberStats.AvgDuration))
	logEvent("Shop", fmt.Sprintf("Avg satisfaction:     %.1f/5", barberStats.AvgRating))
	logEvent("Shop", strings.Repeat("─", 40))
}
