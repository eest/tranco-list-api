package tlapi

import (
	"log"
	"sync"
	"time"

	"golang.org/x/time/rate"
)

// limitData contains information for a specific IP address.
type limitData struct {
	limit *rate.Limiter
	ts    time.Time
}

// rateLimit is a specific instance of rate limiting. The mutex is used
// for protecting access to the "ips" map.
type rateLimit struct {
	rate  rate.Limit
	burst int
	mtx   sync.Mutex
	ips   map[string]*limitData
}

// newRateLimit returns an initialized rateLimit pointer.
func newRateLimit(rate rate.Limit, burst int) *rateLimit {
	return &rateLimit{
		rate:  rate,
		burst: burst,
		ips:   map[string]*limitData{},
	}
}

// updateRateLimit manages the state of an IP address in the rateLimit instance.
func (rl *rateLimit) updateRateLimit(ip string) {
	// Protect access to the rate limit instance.
	rl.mtx.Lock()
	_, exists := rl.ips[ip]
	if !exists {
		// Add new IP.
		rl.ips[ip] = &limitData{
			limit: rate.NewLimiter(rl.rate, rl.burst),
			ts:    time.Now(),
		}
	} else {
		// Update timestamp for existing IP.
		rl.ips[ip].ts = time.Now()
	}
	// We are done with modifications.
	rl.mtx.Unlock()
}

// allow updates rate limit state for the given address and checks if
// the address is allowed or not.
func (rl *rateLimit) allow(ip string) bool {
	// Add or update ip specific limit.
	rl.updateRateLimit(ip)
	// Check if the address is allowed.
	return rl.ips[ip].limit.Allow()
}

// rateLimitCleanup should be running in the background to clean up stale
// address entries over time.
func rateLimitCleanup(rl *rateLimit, cleanupInterval, ageLimit time.Duration) {
	for {
		time.Sleep(cleanupInterval)

		cleanupStart := time.Now()
		seenEntries := 0
		removedEntries := 0

		// Protect access to the rate limit instance
		rl.mtx.Lock()
		for ip, lData := range rl.ips {
			seenEntries++
			if time.Since(lData.ts) > ageLimit {
				delete(rl.ips, ip)
				removedEntries++
			}
		}
		// We are done with modifications.
		rl.mtx.Unlock()

		if seenEntries > 0 {
			log.Printf(
				"rate limit sweep: seen: %d, removed: %d, duration: %s",
				seenEntries,
				removedEntries,
				time.Since(cleanupStart),
			)
		}
	}
}
