package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"net/http"
	"sync"
	"time"
)

// call is one call that the actuator accepted.
type call struct {
	Time      time.Time `json:"time"`
	Machine   string    `json:"machine"`
	Operation string    `json:"operation"`
	// Claim and Token are the headers the request carried, if any. The
	// actuator does not check them; the gatekeeper in front of it does.
	Claim string `json:"claim,omitempty"`
	Token string `json:"token,omitempty"`
}

// actuator is a fake machine provisioning service. It accepts every
// call and records it, as a real service that knows nothing of fencing
// would. It stands behind a gatekeeper in the demo, spec 17.
type actuator struct {
	mu   sync.Mutex
	log  []call
	now  func() time.Time
	seen map[string]string // machine -> last operation
}

func (a *actuator) record(c call) {
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.seen == nil {
		a.seen = map[string]string{}
	}
	a.seen[c.Machine] = c.Operation
	a.log = append(a.log, c)
}

func (a *actuator) calls() []call {
	a.mu.Lock()
	defer a.mu.Unlock()
	return append([]call(nil), a.log...)
}

// handler serves the machine API and GET /log.
func (a *actuator) handler() http.Handler {
	mux := http.NewServeMux()
	for _, op := range []string{"deploy", "power", "release"} {
		mux.HandleFunc("POST /machines/{id}/"+op, func(w http.ResponseWriter, r *http.Request) {
			c := call{Time: a.now(), Machine: r.PathValue("id"), Operation: op,
				Claim: r.Header.Get("Solas-Claim"), Token: r.Header.Get("Solas-Fencing-Token")}
			a.record(c)
			w.Header().Set("Content-Type", "application/json")
			_ = json.NewEncoder(w).Encode(c)
		})
	}
	mux.HandleFunc("GET /machines/{id}", func(w http.ResponseWriter, r *http.Request) {
		a.mu.Lock()
		last := a.seen[r.PathValue("id")]
		a.mu.Unlock()
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]string{"machine": r.PathValue("id"), "last": last})
	})
	mux.HandleFunc("GET /log", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(a.calls())
	})
	return mux
}

func runActuator(ctx context.Context, args []string) error {
	fs := flag.NewFlagSet("actuator", flag.ExitOnError)
	listen := fs.String("listen", ":9100", "address to listen on")
	_ = fs.Parse(args)
	a := &actuator{now: time.Now}
	srv := &http.Server{Addr: *listen, Handler: a.handler(), ReadHeaderTimeout: 5 * time.Second}
	go func() {
		<-ctx.Done()
		_ = srv.Shutdown(context.Background())
	}()
	fmt.Printf("actuator listening on %s; it checks nothing\n", *listen)
	if err := srv.ListenAndServe(); err != http.ErrServerClosed {
		return err
	}
	return nil
}
