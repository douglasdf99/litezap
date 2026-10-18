package mcp

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/douglasdf99/litezap/internal/apierr"
	"github.com/douglasdf99/litezap/internal/service"
)

// REST is a Backend that talks to a running `litezap serve` through the /v1 API (used
// by the `litezap mcp` stdio bridge). The server enforces the token's scope.
type REST struct {
	base  string // without trailing slash
	token string
	hc    *http.Client

	mu    sync.Mutex
	scope *service.Scope // learned from GET /v1/me
}

var _ Backend = (*REST)(nil)

const (
	maxResponseBody = 32 << 20
	maxEventBytes   = 4 << 20
	// restTimeout bounds every call but sends (which wait up to their own timeout) and
	// event waits.
	restTimeout = 60 * time.Second
)

// NewREST returns a client of the LiteZap API at baseURL (e.g. http://127.0.0.1:8787)
// authenticated with token. A nil hc means a default client.
func NewREST(baseURL, token string, hc *http.Client) (*REST, error) {
	u, err := url.Parse(strings.TrimSpace(baseURL))
	if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" {
		return nil, fmt.Errorf("LITEZAP_URL must be an http(s) URL such as http://127.0.0.1:8787")
	}
	if u.User != nil || u.RawQuery != "" || u.Fragment != "" {
		return nil, fmt.Errorf("LITEZAP_URL must not carry credentials, a query or a fragment")
	}
	token = strings.TrimSpace(token)
	if token == "" {
		return nil, fmt.Errorf("LITEZAP_TOKEN is required (the admin token or an instance token)")
	}
	if hc == nil {
		hc = &http.Client{}
	}
	return &REST{base: strings.TrimRight(u.String(), "/"), token: token, hc: hc}, nil
}

func unreachable(base string) *apierr.Error {
	return apierr.New(http.StatusServiceUnavailable, "server_unreachable", "cannot reach the LiteZap server at "+base, true)
}

// request sends one API call and returns the response when it is 2xx; any other status
// is decoded into the API's *apierr.Error.
func (r *REST) request(ctx context.Context, method, path string, q url.Values, body any) (*http.Response, error) {
	var rd io.Reader
	if body != nil {
		b, err := json.Marshal(body)
		if err != nil {
			return nil, err
		}
		rd = bytes.NewReader(b)
	}
	u := r.base + path
	if len(q) > 0 {
		u += "?" + q.Encode()
	}
	req, err := http.NewRequestWithContext(ctx, method, u, rd)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Authorization", "Bearer "+r.token)
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	resp, err := r.hc.Do(req)
	if err != nil {
		if ctx.Err() != nil {
			return nil, apierr.New(http.StatusGatewayTimeout, "worker_timeout", "the LiteZap server did not answer in time", true)
		}
		return nil, unreachable(r.base)
	}
	if resp.StatusCode >= 200 && resp.StatusCode < 300 {
		return resp, nil
	}
	defer resp.Body.Close()
	var env struct {
		Error *apierr.Error `json:"error"`
	}
	b, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if json.Unmarshal(b, &env) == nil && env.Error != nil && env.Error.Code != "" {
		env.Error.Status = resp.StatusCode
		return nil, env.Error
	}
	return nil, apierr.New(resp.StatusCode, "server_error",
		fmt.Sprintf("unexpected HTTP %d from the LiteZap server", resp.StatusCode), resp.StatusCode >= 500)
}

// call runs a request with a timeout and decodes the JSON answer into out (when non-nil).
func (r *REST) call(ctx context.Context, timeout time.Duration, method, path string, q url.Values, body, out any) error {
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	resp, err := r.request(ctx, method, path, q, body)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if out == nil {
		return nil
	}
	if err := json.NewDecoder(io.LimitReader(resp.Body, maxResponseBody)).Decode(out); err != nil {
		return fmt.Errorf("decode %s %s: %w", method, path, err)
	}
	return nil
}

func instPath(id string, rest ...string) string {
	p := "/v1/instances/" + url.PathEscape(id)
	for _, s := range rest {
		p += "/" + s
	}
	return p
}

// chatSegment normalizes a chat or contact (phone or JID) the way the server does, so
// the path segment is always plain.
func chatSegment(raw string) (string, error) {
	jid, err := service.ChatJID(raw)
	if err != nil {
		return "", err
	}
	return url.PathEscape(jid), nil
}

func pageQuery(limit int, cursor string) url.Values {
	q := url.Values{}
	if limit > 0 {
		q.Set("limit", strconv.Itoa(limit))
	}
	if cursor != "" {
		q.Set("cursor", cursor)
	}
	return q
}

func (r *REST) Scope(ctx context.Context) (service.Scope, error) {
	r.mu.Lock()
	if r.scope != nil {
		defer r.mu.Unlock()
		return *r.scope, nil
	}
	r.mu.Unlock() // not held across the call: concurrent first calls each ask, that is fine
	var me struct {
		Admin      bool   `json:"admin"`
		InstanceID string `json:"instance_id"`
	}
	if err := r.call(ctx, restTimeout, "GET", "/v1/me", nil, nil, &me); err != nil {
		return service.Scope{}, err
	}
	if !me.Admin && me.InstanceID == "" {
		return service.Scope{}, errors.New("GET /v1/me returned neither admin nor an instance")
	}
	sc := service.Scope{Admin: me.Admin, InstanceID: me.InstanceID}
	r.mu.Lock()
	r.scope = &sc
	r.mu.Unlock()
	return sc, nil
}

func (r *REST) ListInstances(ctx context.Context) ([]service.InstanceView, error) {
	var out struct {
		Instances []service.InstanceView `json:"instances"`
	}
	err := r.call(ctx, restTimeout, "GET", "/v1/instances", nil, nil, &out)
	return out.Instances, err
}

func (r *REST) GetInstance(ctx context.Context, id string) (service.InstanceView, error) {
	var v service.InstanceView
	err := r.call(ctx, restTimeout, "GET", instPath(id), nil, nil, &v)
	return v, err
}

func (r *REST) QR(ctx context.Context, id string) (string, error) {
	var out struct {
		Code string `json:"code"`
	}
	err := r.call(ctx, restTimeout, "GET", instPath(id, "qr"), nil, nil, &out)
	return out.Code, err
}

func (r *REST) Send(ctx context.Context, id string, in service.SendInput, wait time.Duration) (service.MessageView, error) {
	q := url.Values{}
	if wait > 0 {
		q.Set("wait", "true")
		q.Set("timeout", wait.String())
	}
	var v service.MessageView
	// The server fetches the media before queueing (up to minutes for a large file).
	err := r.call(ctx, wait+5*time.Minute, "POST", instPath(id, "messages"), q, in, &v)
	return v, err
}

func (r *REST) GetMessage(ctx context.Context, id, msgID string) (service.MessageView, error) {
	var v service.MessageView
	err := r.call(ctx, restTimeout, "GET", instPath(id, "messages", url.PathEscape(msgID)), nil, nil, &v)
	return v, err
}

func (r *REST) Chats(ctx context.Context, id string, limit int, cursor string) (service.ChatPage, error) {
	var p service.ChatPage
	err := r.call(ctx, restTimeout, "GET", instPath(id, "chats"), pageQuery(limit, cursor), nil, &p)
	return p, err
}

func (r *REST) ChatMessages(ctx context.Context, id, chat string, limit int, cursor string) (service.MessagePage, error) {
	seg, err := chatSegment(chat)
	if err != nil {
		return service.MessagePage{}, err
	}
	var p service.MessagePage
	err = r.call(ctx, restTimeout, "GET", instPath(id, "chats", seg, "messages"), pageQuery(limit, cursor), nil, &p)
	return p, err
}

func (r *REST) MarkRead(ctx context.Context, id, chat string) error {
	seg, err := chatSegment(chat)
	if err != nil {
		return err
	}
	return r.call(ctx, restTimeout, "POST", instPath(id, "chats", seg, "read"), nil, nil, nil)
}

func (r *REST) Contacts(ctx context.Context, id string) ([]service.ContactView, error) {
	var out struct {
		Contacts []service.ContactView `json:"contacts"`
	}
	err := r.call(ctx, restTimeout, "GET", instPath(id, "contacts"), nil, nil, &out)
	return out.Contacts, err
}

func (r *REST) Contact(ctx context.Context, id, jid string) (service.ContactView, error) {
	seg, err := chatSegment(jid)
	if err != nil {
		return service.ContactView{}, err
	}
	var c service.ContactView
	err = r.call(ctx, restTimeout, "GET", instPath(id, "contacts", seg), nil, nil, &c)
	return c, err
}

func (r *REST) CheckNumbers(ctx context.Context, id string, phones []string) ([]service.NumberView, error) {
	var out struct {
		Results []service.NumberView `json:"results"`
	}
	err := r.call(ctx, restTimeout, "POST", instPath(id, "check-numbers"), nil, map[string]any{"phones": phones}, &out)
	return out.Results, err
}

// WaitEvents reads the instance's SSE stream until timeout, limit or a burst ends.
func (r *REST) WaitEvents(ctx context.Context, id string, types []string, timeout time.Duration, limit int) ([]json.RawMessage, error) {
	sctx, cancel := context.WithCancel(ctx)
	defer cancel()
	q := url.Values{}
	if len(types) > 0 {
		q.Set("types", strings.Join(types, ","))
	}
	// Connecting gets its own bound; the stream itself is bounded by collect.
	connectTimer := time.AfterFunc(restTimeout, cancel)
	resp, err := r.request(sctx, "GET", instPath(id, "events"), q, nil)
	connectTimer.Stop()
	var ae *apierr.Error
	if errors.As(err, &ae) && ae.Status == http.StatusNotFound && ae.Code == "server_error" {
		return nil, apierr.New(http.StatusNotImplemented, "not_implemented", "events are not available on this server", false)
	}
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	evs := make(chan json.RawMessage)
	var ended atomic.Bool // the server closed the stream (error frame or EOF)
	go func() {
		defer close(evs)
		sc := bufio.NewScanner(resp.Body)
		sc.Buffer(make([]byte, 64<<10), maxEventBytes)
		var name string
		var data []byte
		for sc.Scan() {
			line := sc.Text()
			switch {
			case line == "" && name == "error": // e.g. slow_consumer: the server drops us
				ended.Store(true)
				return
			case line == "":
				if len(data) > 0 {
					select {
					case evs <- json.RawMessage(data):
					case <-sctx.Done():
						return
					}
				}
				name, data = "", nil
			case strings.HasPrefix(line, "event:"):
				name = strings.TrimSpace(strings.TrimPrefix(line, "event:"))
			case strings.HasPrefix(line, "data:"):
				if len(data) > 0 {
					data = append(data, '\n')
				}
				data = append(data, strings.TrimPrefix(strings.TrimPrefix(line, "data:"), " ")...)
			}
		}
		if sctx.Err() == nil {
			ended.Store(true)
		}
	}()
	out := collect(sctx, timeout, limit, func(ctx context.Context) (json.RawMessage, bool) {
		select {
		case e, ok := <-evs:
			return e, ok
		case <-ctx.Done():
			return nil, false
		}
	})
	cancel() // closes the stream; the reader goroutine ends
	if len(out) == 0 && ended.Load() {
		return nil, apierr.New(http.StatusServiceUnavailable, "stream_ended",
			"the server closed the event stream (restarting or this client was too slow); call wait_for_events again", true)
	}
	return out, nil
}
