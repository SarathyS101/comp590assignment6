package main

import (
	"fmt"
	"math/rand"
	"strings"
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

func waitingRoom(arriveCh chan Message, nextCustomerCh chan Message, statsCh chan Message, shutdownCh chan struct{}, barberWakeupCh chan Message, barberCustomerReadyCh chan Message, barberNoneWaitingCh chan struct{}, cfg Config) {
	queue := make([]QueueEntry, 0, cfg.WRCapacity)
	barberSleeping := true
	turnedAway := 0

	for {
		select {
		case msg := <-arriveCh:
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
					barberWakeupCh <- Message{
						Kind:       MsgWakeup,
						From:       front.ReplyCh,
						CustomerID: front.CustomerID,
						ArrivalMs:  front.ArrivalMs,
					}
				}
			}

		case msg := <-nextCustomerCh:
			// msg.From needs to have the barber's channel reference
			_ = msg.From
			if len(queue) > 0 {
				front := queue[0]
				queue = queue[1:]
				barberCustomerReadyCh <- Message{
					Kind:       MsgCustomerReady,
					From:       front.ReplyCh,
					CustomerID: front.CustomerID,
					ArrivalMs:  front.ArrivalMs,
				}
			} else {
				barberSleeping = true
				barberNoneWaitingCh <- struct{}{}
			}

		case msg := <-statsCh:
			msg.From <- Message{
				Kind:       MsgStatsReply,
				TurnedAway: turnedAway,
			}

		case <-shutdownCh:
			logEvent("WaitingRoom", "Shutting down")
			return
		}
	}
}

// Barber

func barber(wakeupCh chan Message, customerReadyCh chan Message, noneWaitingCh chan struct{}, statsCh chan Message, shutdownCh chan struct{}, nextCustomerCh chan Message, cfg Config) {
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

		customerCh <- Message{Kind: MsgRateRequest, From: ratingCh}

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

	sendStats := func(msg Message) {
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

	isSleeping := true

	for {
		if isSleeping {
			// Sleep state, it waits for wakeup, stats, or shutdown
			select {
			case msg := <-wakeupCh:
				logEvent("Barber", fmt.Sprintf("Woken up by Customer %d", msg.CustomerID))
				doHaircut(msg.From, msg.CustomerID, msg.ArrivalMs)
				isSleeping = false
			case msg := <-statsCh:
				sendStats(msg)
			case <-shutdownCh:
				logEvent("Barber", "Shutting down")
				return
			}
		} else {
			// Awake state, it asks waiting room for next customer
			nextCustomerCh <- Message{Kind: MsgNextCustomer, From: statsCh}

			// wait for the response or other messages
			select {
			case msg := <-customerReadyCh:
				doHaircut(msg.From, msg.CustomerID, msg.ArrivalMs)
			case <-noneWaitingCh:
				logEvent("Barber", "No customers waiting, going to sleep")
				isSleeping = true
			case msg := <-statsCh:
				sendStats(msg)
			case <-shutdownCh:
				logEvent("Barber", "Shutting down")
				return
			}
		}
	}
}

// Customer

func customer(id int, wrArriveCh chan Message, cfg Config, doneCh chan struct{}) {
	defer func() { doneCh <- struct{}{} }()

	replyCh := make(chan Message, 1)
	arrivalMs := time.Since(startTime).Milliseconds()

	logEvent("Customer", fmt.Sprintf("Customer %d arrives", id))

	wrArriveCh <- Message{
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

	// Admitted , wait for rate request
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

// Shop Owner (main)

func main() {
	cfg := defaultConfig()

	logEvent("Shop", "=== Sleeping Barber Shop Opens ===")

	// Waiting Room channels
	wrArriveCh := make(chan Message, 10)
	wrNextCustomerCh := make(chan Message, 10)
	wrStatsCh := make(chan Message, 10)
	wrShutdownCh := make(chan struct{}, 1)

	// Barber channels
	barberWakeupCh := make(chan Message, 10)
	barberCustomerReadyCh := make(chan Message, 10)
	barberNoneWaitingCh := make(chan struct{}, 10)
	barberStatsCh := make(chan Message, 10)
	barberShutdownCh := make(chan struct{}, 1)

	go waitingRoom(wrArriveCh, wrNextCustomerCh, wrStatsCh, wrShutdownCh, barberWakeupCh, barberCustomerReadyCh, barberNoneWaitingCh, cfg)
	go barber(barberWakeupCh, barberCustomerReadyCh, barberNoneWaitingCh, barberStatsCh, barberShutdownCh, wrNextCustomerCh, cfg)

	doneCh := make(chan struct{}, cfg.TotalCustomers)

	for i := 1; i <= cfg.TotalCustomers; i++ {
		go customer(i, wrArriveCh, cfg, doneCh)
		sleepMs := cfg.ArrivalMinMs + rand.Intn(cfg.ArrivalMaxMs-cfg.ArrivalMinMs+1)
		logEvent("Shop", fmt.Sprintf("Next customer in %dms", sleepMs))
		time.Sleep(time.Duration(sleepMs) * time.Millisecond)
	}

	logEvent("Shop", fmt.Sprintf("All %d customers sent. Waiting for all to finish...", cfg.TotalCustomers))

	// Wait for all customers to complete 
	for i := 0; i < cfg.TotalCustomers; i++ {
		<-doneCh
	}

	logEvent("Shop", "All customers finished")

	// Collect stats
	replyStatsCh := make(chan Message, 1)

	barberStatsCh <- Message{Kind: MsgGetStats, From: replyStatsCh}
	barberStats := <-replyStatsCh

	wrStatsCh <- Message{Kind: MsgGetStats, From: replyStatsCh}
	wrStats := <-replyStatsCh

	// Shutdown
	barberShutdownCh <- struct{}{}
	time.Sleep(100 * time.Millisecond)
	wrShutdownCh <- struct{}{}
	time.Sleep(100 * time.Millisecond)

	// Closing report
	logEvent("Shop", "=== Closing Report ===")
	logEvent("Shop", fmt.Sprintf("Customers served:     %d", barberStats.Served))
	logEvent("Shop", fmt.Sprintf("Customers turned away: %d", wrStats.TurnedAway))
	logEvent("Shop", fmt.Sprintf("Total customers:      %d", barberStats.Served+wrStats.TurnedAway))
	logEvent("Shop", fmt.Sprintf("Avg service duration: %.0fms", barberStats.AvgDuration))
	logEvent("Shop", fmt.Sprintf("Avg satisfaction:     %.1f/5", barberStats.AvgRating))
	logEvent("Shop", strings.Repeat("─", 40))
}
