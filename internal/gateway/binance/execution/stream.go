package binanceexec

import (
	"context"
	"errors"
	"fmt"
	"log"
	"math/rand/v2"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/coder/websocket"

	"trading-system/internal/hft/engine"
)

// userStreamWSPath is appended to Config.WSURL for a listen key.
const userStreamWSPath = "/ws/"

// RunUserStream consumes execution reports from the Binance user data stream
// and sends them to out until ctx is canceled.
//
// Every session acquires a fresh listen key, dials WSURL + "/ws/" + key and
// keeps the key alive with PUT /api/v3/userDataStream every 30 minutes. A lost
// connection, a failed keepalive or a listenKeyExpired event ends the session;
// the loop then reconnects with exponential backoff and jitter, and a new
// listen key.
//
// Malformed frames are logged and skipped, they never tear down the stream.
// Sending to out applies backpressure: the caller must drain the channel
// promptly. The stream returns nil on graceful shutdown (ctx canceled) and
// only returns an error for an unusable call, for example a nil channel.
//
// Events produced while the session is down (between a disconnect and the
// reconnect) are not replayed by Binance. The application is expected to
// reconcile open orders over REST after a reconnect; this package does not do
// that because it is deliberately free of order-state ownership.
func (v *Venue) RunUserStream(ctx context.Context, out chan<- engine.ExecReport) error {
	if out == nil {
		return ErrNilReportChannel
	}
	if ctx == nil {
		return errors.New("binance: nil context")
	}

	attempt := 0
	for {
		if ctx.Err() != nil {
			return nil
		}

		connected, err := v.runSession(ctx, out)
		if ctx.Err() != nil {
			return nil
		}
		if err != nil {
			if connected {
				v.reconnects.Add(1)
			}
			log.Printf("binance: user stream session ended: %v", err)
		}

		if connected {
			attempt = 0
		}
		attempt++
		if !sleepCtx(ctx, v.backoff(attempt)) {
			return nil
		}
	}
}

// runSession runs one listen key + websocket session. connected reports
// whether the websocket handshake succeeded, which the caller uses to decide
// between dial failures and lost connections.
func (v *Venue) runSession(ctx context.Context, out chan<- engine.ExecReport) (connected bool, err error) {
	sessionCtx, cancel := context.WithCancel(ctx)
	defer cancel()

	listenKey, err := v.createListenKey(sessionCtx)
	if err != nil {
		return false, fmt.Errorf("binance: create listen key: %w", err)
	}
	defer v.closeListenKey(listenKey)

	streamURL, err := v.userStreamURL(listenKey)
	if err != nil {
		return false, err
	}

	dialCtx, dialCancel := context.WithTimeout(sessionCtx, wsHandshakeTimeout)
	conn, _, err := websocket.Dial(dialCtx, streamURL, &websocket.DialOptions{
		HTTPClient:      v.cfg.HTTPClient,
		CompressionMode: websocket.CompressionDisabled,
	})
	dialCancel()
	if err != nil {
		return false, fmt.Errorf("binance: dial user stream: %w", err)
	}
	conn.SetReadLimit(userStreamReadLimit)
	defer conn.Close(websocket.StatusNormalClosure, "")

	keepaliveErr := make(chan error, 1)
	go v.keepAlive(sessionCtx, listenKey, cancel, keepaliveErr)

	readErr := v.readUserStream(sessionCtx, conn, out)
	cancel()

	// A keepalive failure cancels the session, so the read loop can only
	// report the contextual websocket error. The keepalive cause is the one
	// worth surfacing.
	select {
	case err := <-keepaliveErr:
		if err != nil {
			return true, err
		}
	default:
	}
	return true, readErr
}

// keepAlive refreshes the listen key until the session ends. On failure it
// reports the error and cancels the session so RunUserStream reconnects with a
// fresh key.
func (v *Venue) keepAlive(ctx context.Context, listenKey string, fail func(), errCh chan<- error) {
	ticker := time.NewTicker(listenKeyKeepaliveInterval)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			errCh <- nil
			return
		case <-ticker.C:
			if err := v.keepAliveListenKey(ctx, listenKey); err != nil {
				errCh <- fmt.Errorf("binance: listen key keepalive: %w", err)
				fail()
				return
			}
		}
	}
}

func (v *Venue) readUserStream(ctx context.Context, conn *websocket.Conn, out chan<- engine.ExecReport) error {
	for {
		messageType, data, err := conn.Read(ctx)
		if err != nil {
			return err
		}
		if messageType != websocket.MessageText {
			continue
		}

		report, err := ParseExecutionReport(data, v.cfg)
		if err != nil {
			switch {
			case errors.Is(err, ErrListenKeyExpired):
				return err
			case errors.Is(err, ErrNotExecutionReport):
				continue
			default:
				v.parseErrors.Add(1)
				log.Printf("binance: user stream frame skipped: %v", err)
				continue
			}
		}

		select {
		case out <- report:
		case <-ctx.Done():
			return ctx.Err()
		}
	}
}

// createListenKey opens a user data stream listen key.
func (v *Venue) createListenKey(ctx context.Context) (string, error) {
	var resp struct {
		ListenKey string `json:"listenKey"`
	}
	if err := v.do(ctx, http.MethodPost, userDataStreamPath, nil, true, &resp); err != nil {
		return "", err
	}
	if resp.ListenKey == "" {
		return "", errors.New("binance: user data stream returned an empty listen key")
	}
	return resp.ListenKey, nil
}

// keepAliveListenKey extends the key's validity by 60 minutes.
func (v *Venue) keepAliveListenKey(ctx context.Context, listenKey string) error {
	params := url.Values{"listenKey": {listenKey}}
	return v.do(ctx, http.MethodPut, userDataStreamPath, params, true, nil)
}

// closeListenKey best-effort invalidates the key on session teardown. It runs
// on a detached context so a canceled session still cleans up.
func (v *Venue) closeListenKey(listenKey string) {
	ctx, cancel := context.WithTimeout(context.Background(), listenKeyCloseTimeout)
	defer cancel()

	params := url.Values{"listenKey": {listenKey}}
	if err := v.do(ctx, http.MethodDelete, userDataStreamPath, params, true, nil); err != nil {
		log.Printf("binance: close listen key: %v", err)
	}
}

// userStreamURL joins WSURL and the listen key without corrupting an existing
// path or query string.
func (v *Venue) userStreamURL(listenKey string) (string, error) {
	u, err := url.Parse(v.cfg.WSURL)
	if err != nil {
		return "", fmt.Errorf("%w: WSURL: %v", ErrInvalidConfig, err)
	}
	if u.Host == "" {
		return "", fmt.Errorf("%w: WSURL has no host", ErrInvalidConfig)
	}

	u.Path = strings.TrimRight(u.Path, "/") + userStreamWSPath + url.PathEscape(listenKey)
	u.RawPath = ""
	return u.String(), nil
}

// backoff returns the reconnect delay for attempt, doubling from ReconnectMin
// to ReconnectMax and jittered by up to +/-20%.
func (v *Venue) backoff(attempt int) time.Duration {
	delay := v.cfg.ReconnectMin
	for i := 1; i < attempt && delay < v.cfg.ReconnectMax; i++ {
		delay *= 2
	}
	if delay > v.cfg.ReconnectMax {
		delay = v.cfg.ReconnectMax
	}

	jitter := int64(delay / 5)
	if jitter <= 0 {
		return delay
	}
	jittered := delay + time.Duration(rand.Int64N(2*jitter+1)-jitter)
	if jittered <= 0 {
		return time.Millisecond
	}
	return jittered
}

func sleepCtx(ctx context.Context, d time.Duration) bool {
	timer := time.NewTimer(d)
	defer timer.Stop()

	select {
	case <-ctx.Done():
		return false
	case <-timer.C:
		return true
	}
}
