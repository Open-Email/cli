// Package eventstream combines the mail and PIM sockets without depending on
// core's transitional mail-socket relay. Each socket owns its reconnect loop;
// callbacks are serialized, and returning from Run joins every reader.
package eventstream

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math/rand/v2"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"

	"github.com/coder/websocket"
)

type Kind string

const (
	Mail Kind = "mail"
	PIM  Kind = "PIM"
)

type Config struct {
	APIURL, Token, IdentityID string
	Reconnect                 bool
}

type Update struct {
	Kind      Kind
	Frame     []byte
	Connected bool
	Terminal  bool
	RetryIn   time.Duration
	Err       error
}

type Failure struct {
	Kind       Kind
	HTTPStatus int
	CloseCode  websocket.StatusCode
	Retryable  bool
	Message    string
}

func (e *Failure) Error() string      { return string(e.Kind) + " events: " + e.Message }
func (e *Failure) Unauthorized() bool { return e.HTTPStatus == 401 || e.CloseCode == 4403 }
func (e *Failure) Gone() bool         { return e.HTTPStatus == 410 || e.CloseCode == 4410 }

func URL(apiURL, identityID string, kind Kind) (string, error) {
	u, err := url.Parse(strings.TrimRight(apiURL, "/"))
	if err != nil {
		return "", err
	}
	switch u.Scheme {
	case "http", "ws":
		u.Scheme = "ws"
	case "https", "wss":
		u.Scheme = "wss"
	default:
		return "", fmt.Errorf("unsupported events URL scheme %q", u.Scheme)
	}
	q := u.Query()
	basePath := strings.TrimRight(u.Path, "/")
	escapedBase := strings.TrimRight(u.EscapedPath(), "/")
	escapedID := url.PathEscape(identityID)
	if kind == PIM {
		u.Path = basePath + "/api/v1/identities/" + identityID + "/pim/events"
		u.RawPath = escapedBase + "/api/v1/identities/" + escapedID + "/pim/events"
		q.Del("pim")
	} else {
		u.Path = basePath + "/api/v1/mailboxes/" + identityID + "/events"
		u.RawPath = escapedBase + "/api/v1/mailboxes/" + escapedID + "/events"
		q.Set("pim", "off")
	}
	u.RawQuery = q.Encode()
	return u.String(), nil
}

// Run invokes consume on one goroutine, so stdout, --exec, and UI delivery
// cannot race. A true return stops both streams before another frame is handed
// to consume. A PIM-only permission refusal disables that optional half; losing
// the primary mail stream, the credential, or the identity ends the subscription.
func Run(parent context.Context, cfg Config, consume func(Update) bool) error {
	ctx, cancel := context.WithCancel(parent)
	var workers sync.WaitGroup
	defer func() { cancel(); workers.Wait() }()
	updates := make(chan Update, 32)
	for _, kind := range []Kind{Mail, PIM} {
		address, err := URL(cfg.APIURL, cfg.IdentityID, kind)
		if err != nil {
			return err
		}
		workers.Add(1)
		go func() {
			defer workers.Done()
			runSocket(ctx, cfg, kind, address, updates)
		}()
	}
	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case update := <-updates:
			if consume(update) {
				return nil
			}
			var failure *Failure
			if update.Terminal && (update.Kind == Mail ||
				(errors.As(update.Err, &failure) && (failure.Unauthorized() || failure.Gone()))) {
				return update.Err
			}
		}
	}
}

func emit(ctx context.Context, updates chan<- Update, update Update) bool {
	select {
	case updates <- update:
		return true
	case <-ctx.Done():
		return false
	}
}

func runSocket(ctx context.Context, cfg Config, kind Kind, address string, updates chan<- Update) {
	const floor = 500 * time.Millisecond
	backoff := floor
	for ctx.Err() == nil {
		started := time.Now()
		failure := session(ctx, cfg.Token, kind, address, updates)
		if ctx.Err() != nil {
			return
		}
		retry := cfg.Reconnect && failure.Retryable
		if time.Since(started) >= 30*time.Second {
			backoff = floor
		}
		wait := backoff/2 + time.Duration(rand.Int64N(int64(backoff/2)+1))
		update := Update{Kind: kind, Err: failure, Terminal: !retry}
		if retry {
			update.RetryIn = wait
		}
		if !emit(ctx, updates, update) || !retry {
			return
		}
		timer := time.NewTimer(wait)
		select {
		case <-ctx.Done():
			timer.Stop()
			return
		case <-timer.C:
		}
		backoff = min(backoff*2, 15*time.Second)
	}
}

func session(ctx context.Context, token string, kind Kind, address string, updates chan<- Update) *Failure {
	dialCtx, cancelDial := context.WithTimeout(ctx, 30*time.Second)
	conn, resp, err := websocket.Dial(dialCtx, address, &websocket.DialOptions{
		HTTPHeader: http.Header{"Authorization": {"Bearer " + token}},
	})
	cancelDial()
	if err != nil {
		failure := &Failure{Kind: kind, Retryable: true, Message: err.Error()}
		if resp != nil {
			failure.HTTPStatus = resp.StatusCode
			failure.Message = fmt.Sprintf("upgrade refused (HTTP %d)", resp.StatusCode)
			if resp.StatusCode >= 400 && resp.StatusCode < 500 && resp.StatusCode != 429 {
				failure.Retryable = false
			}
			if resp.Body != nil {
				// A missing PIM store is a recoverable condition only when the
				// server says so. A generic 404 is an authorization/not-found refusal.
				if kind == PIM && resp.StatusCode == 404 {
					var body struct {
						Error string `json:"error"`
					}
					if json.NewDecoder(io.LimitReader(resp.Body, 4096)).Decode(&body) == nil && body.Error == "pim_unprovisioned" {
						failure.Retryable = true
						failure.Message = "store not provisioned yet"
					}
				}
				_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, 4096))
				_ = resp.Body.Close()
			}
		}
		return failure
	}
	defer conn.CloseNow()
	conn.SetReadLimit(1 << 20)
	if !emit(ctx, updates, Update{Kind: kind, Connected: true}) {
		_ = conn.Close(websocket.StatusNormalClosure, "")
		return &Failure{Kind: kind, Message: ctx.Err().Error()}
	}
	readCtx, stop := context.WithCancel(ctx)
	pingDone := make(chan struct{})
	defer func() { stop(); <-pingDone }()
	go func() {
		defer close(pingDone)
		ticker := time.NewTicker(30 * time.Second)
		defer ticker.Stop()
		for {
			select {
			case <-readCtx.Done():
				return
			case <-ticker.C:
				if err := conn.Write(readCtx, websocket.MessageText, []byte("ping")); err != nil {
					stop()
					return
				}
			}
		}
	}()
	for {
		typ, data, err := conn.Read(readCtx)
		if err != nil {
			code := websocket.CloseStatus(err)
			failure := &Failure{Kind: kind, CloseCode: code, Retryable: true, Message: err.Error()}
			switch code {
			case 4401:
				failure.Message = "session expired"
			case 4403:
				failure.Retryable, failure.Message = false, "credential revoked"
			case 4410:
				failure.Retryable, failure.Message = false, "identity gone"
			}
			return failure
		}
		line := strings.TrimSpace(string(data))
		if typ != websocket.MessageText || line == "" || line == "pong" {
			continue
		}
		if !emit(ctx, updates, Update{Kind: kind, Frame: []byte(line)}) {
			_ = conn.Close(websocket.StatusNormalClosure, "")
			return &Failure{Kind: kind, Message: ctx.Err().Error()}
		}
	}
}
