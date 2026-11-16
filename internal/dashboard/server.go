package dashboard

import (
	"context"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"log/slog"
	"net"
	"net/http"
	"strconv"
	"sync"
	"time"
)

// Auth is the optional basic auth of the server.
type Auth struct{ User, Password string }

// Enabled tells whether a password is set.
func (a Auth) Enabled() bool { return a.Password != "" }

// CheckListen refuses an address reachable from outside without a password.
func CheckListen(addr string, a Auth) error {
	host, _, err := net.SplitHostPort(addr)
	if err != nil {
		return fmt.Errorf("dashboard addr %q: %w", addr, err)
	}
	ip := net.ParseIP(host)
	if host == "localhost" || (ip != nil && ip.IsLoopback()) || a.Enabled() {
		return nil
	}
	return fmt.Errorf("dashboard addr %q is not loopback: set MACRO_DASHBOARD_PASSWORD or listen on 127.0.0.1", addr)
}

// statusTTL is how long /api/status is reused: building it reads every series.
const statusTTL = 30 * time.Second

// Server is the HTTP face of Data.
type Server struct {
	Data *Data
	Auth Auth
	// Static is the frontend.
	Static fs.FS
	Log    *slog.Logger

	heavy chan struct{} // limits concurrent series and report answers

	mu       sync.Mutex
	status   StatusAnswer
	statusAt time.Time
}

// Handler is the whole site with its middleware.
func (s *Server) Handler() http.Handler {
	s.heavy = make(chan struct{}, 4)
	mux := http.NewServeMux()
	mux.HandleFunc("/api/meta", s.meta)
	mux.HandleFunc("/api/series/{source}/{id}", s.limited(s.series))
	mux.HandleFunc("/api/reports/{region}/{agent}", s.limited(s.reports))
	mux.HandleFunc("/api/calendar", s.calendar)
	mux.HandleFunc("/api/status", s.statusAnswer)
	mux.HandleFunc("/api/", func(w http.ResponseWriter, _ *http.Request) { http.Error(w, "not found", http.StatusNotFound) })
	mux.HandleFunc("/mode.js", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/javascript; charset=utf-8")
		fmt.Fprint(w, liveModeScript)
	})
	mux.Handle("/", http.FileServerFS(s.Static))
	return s.secure(mux)
}

const (
	liveModeScript   = `window.MACRO_MODE = "live";` + "\n"
	staticModeScript = `window.MACRO_MODE = "static";` + "\n"
	csp              = "default-src 'none'; script-src 'self'; style-src 'self' 'unsafe-inline'; img-src 'self' data:; font-src 'self'; connect-src 'self'; base-uri 'none'; form-action 'none'; frame-ancestors 'none'"
)

func (s *Server) secure(next http.Handler) http.Handler {
	want := sha256.Sum256([]byte(s.Auth.User + "\x00" + s.Auth.Password))
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet && r.Method != http.MethodHead {
			w.Header().Set("Allow", "GET, HEAD")
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}
		h := w.Header()
		h.Set("Content-Security-Policy", csp)
		h.Set("X-Content-Type-Options", "nosniff")
		h.Set("Referrer-Policy", "no-referrer")
		if s.Auth.Enabled() {
			user, pass, _ := r.BasicAuth()
			got := sha256.Sum256([]byte(user + "\x00" + pass))
			if subtle.ConstantTimeCompare(got[:], want[:]) != 1 {
				h.Set("WWW-Authenticate", `Basic realm="macro dashboard", charset="UTF-8"`)
				http.Error(w, "unauthorized", http.StatusUnauthorized)
				return
			}
		}
		next.ServeHTTP(w, r)
	})
}

// limited runs heavy answers a few at a time, so many open charts wait
// instead of filling the memory.
func (s *Server) limited(next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		ctx, cancel := context.WithTimeout(r.Context(), 20*time.Second)
		defer cancel()
		select {
		case s.heavy <- struct{}{}:
			defer func() { <-s.heavy }()
			next(w, r.WithContext(ctx))
		case <-ctx.Done():
			http.Error(w, "busy", http.StatusServiceUnavailable)
		}
	}
}

func (s *Server) meta(w http.ResponseWriter, _ *http.Request) { s.writeJSON(w, s.Data.Meta()) }

func (s *Server) series(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	var from time.Time
	if v := q.Get("from"); v != "" {
		var err error
		if from, err = time.Parse(time.DateOnly, v); err != nil {
			http.Error(w, "bad from: want YYYY-MM-DD", http.StatusBadRequest)
			return
		}
	}
	ans, err := s.Data.Series(r.Context(), r.PathValue("source"), r.PathValue("id"), q.Get("transform"), from)
	if err != nil {
		s.fail(w, r, err)
		return
	}
	s.writeJSON(w, ans)
}

func (s *Server) reports(w http.ResponseWriter, r *http.Request) {
	ans, err := s.Data.Reports(r.PathValue("region"), r.PathValue("agent"), r.URL.Query().Get("week"))
	if err != nil {
		s.fail(w, r, err)
		return
	}
	s.writeJSON(w, ans)
}

func (s *Server) calendar(w http.ResponseWriter, r *http.Request) {
	days := 14
	if v := r.URL.Query().Get("days"); v != "" {
		n, err := strconv.Atoi(v)
		if err != nil || n < 1 || n > 365 {
			http.Error(w, "bad days: want 1..365", http.StatusBadRequest)
			return
		}
		days = n
	}
	ans, err := s.Data.Calendar(r.Context(), days)
	if err != nil {
		s.fail(w, r, err)
		return
	}
	s.writeJSON(w, ans)
}

func (s *Server) statusAnswer(w http.ResponseWriter, r *http.Request) {
	if s.Data.Demo {
		http.NotFound(w, r)
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if now := s.Data.Now(); s.statusAt.IsZero() || now.Sub(s.statusAt) > statusTTL {
		ans, err := s.Data.Status(r.Context())
		if err != nil {
			s.fail(w, r, err)
			return
		}
		s.status, s.statusAt = ans, now
	}
	s.writeJSON(w, s.status)
}

// fail answers without internals: paths and SQL stay in the log.
func (s *Server) fail(w http.ResponseWriter, r *http.Request, err error) {
	switch {
	case errors.Is(err, ErrNotFound):
		http.Error(w, "not found", http.StatusNotFound)
	case errors.Is(err, ErrBadRequest):
		http.Error(w, "bad request", http.StatusBadRequest)
	default:
		s.Log.Error("dashboard request failed", "source", "dashboard", "path", r.URL.Path, "err", redact(err.Error()))
		http.Error(w, "internal error", http.StatusInternalServerError)
	}
}

func (s *Server) writeJSON(w http.ResponseWriter, v any) {
	body, err := json.Marshal(v)
	if err != nil {
		s.Log.Error("dashboard encode failed", "source", "dashboard", "err", err)
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	_, _ = w.Write(body)
}

// Serve listens on addr until ctx ends.
func (s *Server) Serve(ctx context.Context, addr string) error {
	srv := &http.Server{
		Addr: addr, Handler: s.Handler(),
		ReadHeaderTimeout: 5 * time.Second, ReadTimeout: 10 * time.Second,
		WriteTimeout: 40 * time.Second, IdleTimeout: 60 * time.Second, MaxHeaderBytes: 16 << 10,
	}
	done := make(chan error, 1)
	go func() {
		<-ctx.Done()
		stop, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		done <- srv.Shutdown(stop)
	}()
	if err := srv.ListenAndServe(); !errors.Is(err, http.ErrServerClosed) {
		return fmt.Errorf("dashboard serve: %w", err)
	}
	return <-done
}
