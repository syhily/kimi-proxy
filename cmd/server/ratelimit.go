package main

import (
	"math"
	"sync"
	"time"
)

// maxIPBuckets bounds the per-IP rate-limit table. When the table grows past
// this, buckets idle for over 10 minutes are dropped.
const maxIPBuckets = 65536

// ipLimiter is a per-IP token bucket for new connections: each IP may open up
// to burst connections instantly, then rate per second. Peers that exceed it
// are rejected cheaply, before any TLS or smux work happens.
type ipLimiter struct {
	mu      sync.Mutex
	rate    float64 // tokens added per second
	burst   float64
	buckets map[string]*ipBucket
}

type ipBucket struct {
	tokens float64
	last   time.Time
}

func newIPLimiter(rate, burst float64) *ipLimiter {
	return &ipLimiter{rate: rate, burst: burst, buckets: make(map[string]*ipBucket)}
}

// allow consumes one token for ip, reporting whether the connection may
// proceed. It is cheap and lock-scoped; callers must not hold it while doing
// network I/O.
func (l *ipLimiter) allow(ip string) bool {
	l.mu.Lock()
	defer l.mu.Unlock()
	now := time.Now()
	b, ok := l.buckets[ip]
	if !ok {
		b = &ipBucket{tokens: l.burst, last: now}
		l.buckets[ip] = b
	} else {
		b.tokens = math.Min(l.burst, b.tokens+now.Sub(b.last).Seconds()*l.rate)
		b.last = now
	}
	if b.tokens >= 1 {
		b.tokens--
		return true
	}
	return false
}

// maxBannedIPs bounds the ban table; when full, the oldest entry is evicted.
const maxBannedIPs = 10000

// ipBan is a temporary per-IP ban list. It is only populated on the TCP
// transport, where the source IP is real (UDP source addresses can be
// spoofed, so banning there would let an attacker knock a victim's IP out of
// service).
type ipBan struct {
	mu    sync.Mutex
	until map[string]time.Time
}

func newIPBan() *ipBan {
	return &ipBan{until: make(map[string]time.Time)}
}

// ban marks ip as banned for d.
func (b *ipBan) ban(ip string, d time.Duration) {
	if d <= 0 {
		return
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	if len(b.until) >= maxBannedIPs {
		var oldest string
		var oldestAt time.Time
		for k, v := range b.until {
			if oldest == "" || v.Before(oldestAt) {
				oldest, oldestAt = k, v
			}
		}
		delete(b.until, oldest)
	}
	b.until[ip] = time.Now().Add(d)
}

// blocked reports whether ip is currently banned, lazily expiring entries.
func (b *ipBan) blocked(ip string) bool {
	b.mu.Lock()
	defer b.mu.Unlock()
	until, ok := b.until[ip]
	if !ok {
		return false
	}
	if time.Now().After(until) {
		delete(b.until, ip)
		return false
	}
	return true
}
