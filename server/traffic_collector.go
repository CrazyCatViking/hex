package hex

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"math"
	"net"
	"net/http"
	"slices"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"
)

// TrafficCollector receives NGINX syslog datagrams on loopback only. A bounded
// queue isolates page delivery from database latency. Successful batches are
// durable/idempotent; UDP delivery itself is best effort, not billing-grade.
type TrafficCollector struct {
	conn          *net.UDPConn
	cancel        context.CancelFunc
	done          chan struct{}
	closeOnce     sync.Once
	running       atomic.Bool
	received      atomic.Uint64
	recorded      atomic.Uint64
	rejected      atomic.Uint64
	dropped       atomic.Uint64
	writeFailures atomic.Uint64
	lastReceived  atomic.Int64
	mu            sync.Mutex
	lastError     string
}

type TrafficCollectorStatus struct {
	Running       bool      `json:"running"`
	Received      uint64    `json:"received"`
	Recorded      uint64    `json:"recorded"`
	Rejected      uint64    `json:"rejected"`
	Dropped       uint64    `json:"dropped"`
	WriteFailures uint64    `json:"writeFailures"`
	LastReceived  time.Time `json:"lastReceived,omitzero"`
	LastError     string    `json:"lastError,omitempty"`
}

func StartTrafficCollector(ctx context.Context, address string, store AnalyticsStore) (*TrafficCollector, error) {
	if store == nil {
		return nil, errors.New("traffic collector requires analytics storage")
	}
	resolved, err := net.ResolveUDPAddr("udp", address)
	if err != nil || resolved == nil || !resolved.IP.IsLoopback() {
		return nil, fmt.Errorf("traffic collector address must be loopback: %q", address)
	}
	conn, err := net.ListenUDP("udp", resolved)
	if err != nil {
		return nil, fmt.Errorf("listen for traffic analytics: %w", err)
	}
	if err := conn.SetReadBuffer(1 << 20); err != nil {
		conn.Close()
		return nil, fmt.Errorf("set traffic socket buffer: %w", err)
	}
	workerContext, cancel := context.WithCancel(ctx)
	collector := &TrafficCollector{conn: conn, cancel: cancel, done: make(chan struct{})}
	collector.running.Store(true)
	queue := make(chan TrafficEvent, 4096)
	go collector.read(queue)
	go collector.write(workerContext, store, queue)
	go func() {
		<-workerContext.Done()
		if err := conn.Close(); err != nil && !errors.Is(err, net.ErrClosed) {
			slog.Error("close traffic socket", "error", err)
		}
	}()
	return collector, nil
}

func (c *TrafficCollector) Address() string { return c.conn.LocalAddr().String() }

func (c *TrafficCollector) Close() error {
	c.closeOnce.Do(c.cancel)
	<-c.done
	return nil
}

func (c *TrafficCollector) Status() TrafficCollectorStatus {
	c.mu.Lock()
	lastError := c.lastError
	c.mu.Unlock()
	status := TrafficCollectorStatus{
		Running: c.running.Load(), Received: c.received.Load(), Recorded: c.recorded.Load(),
		Rejected: c.rejected.Load(), Dropped: c.dropped.Load(), WriteFailures: c.writeFailures.Load(), LastError: lastError,
	}
	if at := c.lastReceived.Load(); at != 0 {
		status.LastReceived = time.Unix(0, at).UTC()
	}
	return status
}

func (c *TrafficCollector) failure(err error) {
	c.mu.Lock()
	c.lastError = err.Error()
	c.mu.Unlock()
	slog.Error("traffic analytics collector", "error", err)
}

func (c *TrafficCollector) read(queue chan<- TrafficEvent) {
	defer close(queue)
	defer c.running.Store(false)
	buffer := make([]byte, 8192)
	for {
		n, source, err := c.conn.ReadFromUDP(buffer)
		if err != nil {
			if !errors.Is(err, net.ErrClosed) {
				c.failure(err)
			}
			return
		}
		c.received.Add(1)
		c.lastReceived.Store(time.Now().UnixNano())
		if !source.IP.IsLoopback() || n == len(buffer) {
			c.rejected.Add(1)
			continue
		}
		event, err := parseTrafficLog(buffer[:n], time.Now())
		if err != nil {
			c.rejected.Add(1)
			continue
		}
		select {
		case queue <- event:
		default:
			c.dropped.Add(1)
		}
	}
}

func (c *TrafficCollector) write(ctx context.Context, store AnalyticsStore, queue <-chan TrafficEvent) {
	defer close(c.done)
	ticker := time.NewTicker(250 * time.Millisecond)
	defer ticker.Stop()
	batch := make([]TrafficEvent, 0, 128)
	flush := func() bool {
		if len(batch) == 0 {
			return true
		}
		slices.SortFunc(batch, func(a, b TrafficEvent) int { return a.At.Compare(b.At) })
		writeContext, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		err := store.RecordTraffic(writeContext, batch)
		cancel()
		if err != nil {
			c.writeFailures.Add(1)
			c.failure(err)
			return false
		}
		c.recorded.Add(uint64(len(batch)))
		batch = batch[:0]
		return true
	}
	for {
		if len(batch) == cap(batch) && !flush() {
			select {
			case <-ctx.Done():
				c.dropped.Add(uint64(len(batch)))
				for range queue {
					c.dropped.Add(1)
				}
				return
			case <-time.After(time.Second):
				continue
			}
		}
		select {
		case event, open := <-queue:
			if !open {
				if !flush() {
					c.dropped.Add(uint64(len(batch)))
				}
				return
			}
			batch = append(batch, event)
		case <-ticker.C:
			flush()
		}
	}
}

type nginxTrafficLog struct {
	Version     int    `json:"version"`
	ID          string `json:"id"`
	Time        string `json:"time"`
	Site        string `json:"site"`
	User        string `json:"user"`
	Method      string `json:"method"`
	Status      string `json:"status"`
	Bytes       string `json:"bytes"`
	Duration    string `json:"duration"`
	Destination string `json:"destination"`
	ContentType string `json:"contentType"`
	API         string `json:"api"`
}

func parseTrafficLog(data []byte, now time.Time) (TrafficEvent, error) {
	// NGINX prefixes JSON with an RFC3164 syslog envelope.
	start := bytes.IndexByte(data, '{')
	if start < 0 {
		return TrafficEvent{}, errors.New("missing traffic JSON")
	}
	var log nginxTrafficLog
	if err := json.Unmarshal(data[start:], &log); err != nil {
		return TrafficEvent{}, err
	}
	if log.Version != 1 || !siteNamePattern.MatchString(log.Site) || len(log.ID) != 32 {
		return TrafficEvent{}, errors.New("invalid traffic identity")
	}
	for _, character := range log.ID {
		if !strings.ContainsRune("0123456789abcdef", character) {
			return TrafficEvent{}, errors.New("invalid request ID")
		}
	}
	seconds, err := strconv.ParseFloat(log.Time, 64)
	if err != nil || math.IsNaN(seconds) || math.IsInf(seconds, 0) || seconds < 0 || seconds > float64(now.Add(5*time.Minute).Unix()) {
		return TrafficEvent{}, errors.New("invalid traffic time")
	}
	at := time.UnixMilli(int64(math.Round(seconds * 1000))).UTC()
	if at.Before(now.AddDate(0, 0, -7)) {
		return TrafficEvent{}, errors.New("traffic event is too old")
	}
	status, err := strconv.Atoi(log.Status)
	if err != nil || status < 100 || status > 599 {
		return TrafficEvent{}, errors.New("invalid traffic status")
	}
	size, err := strconv.ParseInt(log.Bytes, 10, 64)
	if err != nil || size < 0 || size > 1<<50 {
		return TrafficEvent{}, errors.New("invalid traffic bytes")
	}
	duration, err := strconv.ParseFloat(log.Duration, 64)
	if err != nil || math.IsNaN(duration) || math.IsInf(duration, 0) || duration < 0 || duration > 7*24*60*60 {
		return TrafficEvent{}, errors.New("invalid traffic duration")
	}
	userID := ""
	if log.User != "" && log.User != "-" {
		decoded, err := base64.RawURLEncoding.DecodeString(log.User)
		if err != nil || len(decoded) == 0 || len(decoded) > 512 {
			return TrafficEvent{}, errors.New("invalid analytics visitor")
		}
		userID = string(decoded)
	}
	if log.API != "0" && log.API != "1" {
		return TrafficEvent{}, errors.New("invalid traffic classification")
	}
	isDocument := log.Destination == "document" || log.Destination == "iframe" ||
		(log.Destination == "" || log.Destination == "-") &&
			(strings.HasPrefix(log.ContentType, "text/html") || strings.HasPrefix(log.ContentType, "application/xhtml+xml"))
	success := status >= 200 && status < 300 || status == http.StatusNotModified
	return TrafficEvent{
		ID: log.ID, At: at, Site: log.Site, UserID: userID, Status: status, Bytes: size,
		DurationMillis: int64(math.Round(duration * 1000)), PageView: log.Method == "GET" && log.API == "0" && success && isDocument,
	}, nil
}
