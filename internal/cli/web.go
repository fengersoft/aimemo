package cli

import (
	"context"
	"embed"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"log"
	"net"
	"net/http"
	"os"
	"os/exec"
	"os/signal"
	"runtime"
	"strconv"
	"strings"
	"time"

	"aimemo/internal/store"
)

//go:embed web
var webFS embed.FS

// WebStore limits what the web UI can access to read-only operations.
type WebStore interface {
	List(f store.Filter, limit, offset int, sort string) (*store.ListResult, error)
	Search(query string, f store.Filter, limit, offset int, sort string, loose bool) (*store.SearchResult, error)
	Get(id int64, workspace string) (*store.Memory, error)
	Stats(workspace string) (*store.Stats, error)
	ListCategories(sort, workspace string) ([]store.Category, error)
}

type webServer struct {
	store     WebStore
	workspace string
}

func runWeb(g globalOpts, args []string, stdout, stderr io.Writer) int {
	cfg := loadConfig()
	defaultAddr := "127.0.0.1:8080"
	if v := os.Getenv("AIMEMO_WEB_ADDR"); v != "" {
		defaultAddr = v
	} else if cfg.Web.Addr != "" {
		defaultAddr = cfg.Web.Addr
	}
	defaultOpen := cfg.Web.Open || envBool("AIMEMO_WEB_OPEN")
	fs := flag.NewFlagSet("web", flag.ContinueOnError)
	addr := fs.String("addr", defaultAddr, "listen address (default localhost only)")
	open := fs.Bool("open", defaultOpen, "open browser")
	if _, err := parseFlagSet(fs, args, stdout); err != nil {
		if errors.Is(err, errHelpRequested) {
			return exitOK
		}
		return fail(g, stderr, newError(codeInvalidArgs, "%v", err))
	}

	host, _, err := net.SplitHostPort(*addr)
	if err != nil {
		return fail(g, stderr, newError(codeInvalidArgs, "invalid --addr"))
	}
	if host != "127.0.0.1" && host != "localhost" && host != "::1" {
		return fail(g, stderr, newError(codeInvalidArgs, "only localhost is allowed"))
	}

	s, err := openStore(g)
	if err != nil {
		return fail(g, stderr, err)
	}

	ws := &webServer{store: s, workspace: g.workspace}
	handler := hostCheck(ws.routes())
	handler = securityHeaders(handler)
	handler = accessLog(handler)
	srv := &http.Server{
		Addr:              *addr,
		Handler:           handler,
		ReadHeaderTimeout: 5 * time.Second,
		ReadTimeout:       10 * time.Second,
		WriteTimeout:      30 * time.Second,
		IdleTimeout:       60 * time.Second,
	}

	listener, err := net.Listen("tcp", *addr)
	if err != nil {
		return fail(g, stderr, newError(codeIOError, "%v", err))
	}
	fmt.Fprintf(stdout, "aimemo web listening on http://%s (workspace=%q)\n", *addr, g.workspace)
	fmt.Fprintln(stdout, "press Ctrl+C to stop")

	if *open {
		_ = openBrowser("http://" + *addr)
	}

	ctxSig, stop := signal.NotifyContext(context.Background(), os.Interrupt)
	defer stop()
	go func() {
		<-ctxSig.Done()
		shctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		defer cancel()
		_ = srv.Shutdown(shctx)
	}()

	err = srv.Serve(listener)
	if err != nil && err != http.ErrServerClosed {
		return fail(g, stderr, newError(codeIOError, "%v", err))
	}
	s.Close()
	return exitOK
}

// routes builds the HTTP handler tree. It is separate from runWeb so tests
// can exercise it without opening a listener.
func (ws *webServer) routes() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("/", ws.handleIndex)
	mux.HandleFunc("/healthz", ws.handleHealthz)
	mux.HandleFunc("/api/list", ws.handleList)
	mux.HandleFunc("/api/get", ws.handleGet)
	mux.HandleFunc("/api/stats", ws.handleStats)
	mux.HandleFunc("/api/health", ws.handleAPIHealth)
	mux.HandleFunc("/api/add", ws.handleAdd)
	mux.HandleFunc("/api/update", ws.handleUpdate)
	mux.HandleFunc("/api/delete", ws.handleDelete)
	mux.HandleFunc("/api/conflicts", ws.handleConflicts)
	mux.HandleFunc("/static/", ws.handleStatic)
	return mux
}

func (ws *webServer) handleIndex(w http.ResponseWriter, r *http.Request) {
	if r.URL.Path != "/" {
		http.NotFound(w, r)
		return
	}
	w.Header().Set("Cache-Control", "no-cache")
	b, err := webFS.ReadFile("web/index.html")
	if err != nil {
		http.Error(w, "missing web assets", http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	_, _ = w.Write(b)
}

func (ws *webServer) handleHealthz(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	_, _ = w.Write([]byte(`{"ok":true}`))
}

func (ws *webServer) handleAPIHealth(w http.ResponseWriter, r *http.Request) {
	type healther interface{ Health() (*store.Health, error) }
	h, err := ws.store.(healther).Health()
	if err != nil {
		log.Printf("web health error: %v", err)
		writeJSON(w, http.StatusInternalServerError, map[string]any{"ok": false, "error": "internal error"})
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "data": h})
}

func (ws *webServer) handleStatic(w http.ResponseWriter, r *http.Request) {
	p := strings.TrimPrefix(r.URL.Path, "/static/")
	b, err := webFS.ReadFile("web/" + p)
	if err != nil {
		http.NotFound(w, r)
		return
	}
	if strings.HasSuffix(p, ".css") {
		w.Header().Set("Content-Type", "text/css; charset=utf-8")
	} else if strings.HasSuffix(p, ".js") {
		w.Header().Set("Content-Type", "text/javascript; charset=utf-8")
	}
	_, _ = w.Write(b)
}

func (ws *webServer) handleList(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	limit, _ := strconv.Atoi(q.Get("limit"))
	offset, _ := strconv.Atoi(q.Get("offset"))
	if limit <= 0 {
		limit = 50
	}
	if limit > 200 {
		limit = 200
	}
	if offset < 0 {
		offset = 0
	}
	cat := q.Get("category")
	typ := q.Get("type")
	search := strings.TrimSpace(q.Get("q"))

	f := store.Filter{Workspace: ws.workspace}
	if cat != "" {
		f.Categories = []string{cat}
	}
	if typ != "" {
		f.Types = []string{typ}
	}

	if search != "" {
		res, err := ws.store.Search(search, f, limit, offset, "created_at", false)
		if err != nil {
			log.Printf("web search error: %v", err)
			writeJSON(w, http.StatusBadRequest, map[string]any{"ok": false, "error": "invalid search query"})
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{"ok": true, "data": map[string]any{
			"items": res.Items, "total": res.Total, "limit": limit, "offset": offset,
		}})
		return
	}

	res, err := ws.store.List(f, limit, offset, "created_at")
	if err != nil {
		log.Printf("web list error: %v", err)
		writeJSON(w, http.StatusInternalServerError, map[string]any{"ok": false, "error": "internal error"})
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "data": map[string]any{
		"items": res.Items, "total": res.Total, "limit": limit, "offset": offset,
	}})
}

func (ws *webServer) handleGet(w http.ResponseWriter, r *http.Request) {
	id, err := strconv.ParseInt(r.URL.Query().Get("id"), 10, 64)
	if err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]any{"ok": false, "error": "invalid id"})
		return
	}
	m, err := ws.store.Get(id, ws.workspace)
	if err != nil {
		log.Printf("web get error id=%d: %v", id, err)
		writeJSON(w, http.StatusNotFound, map[string]any{"ok": false, "error": "record not found in current workspace"})
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "data": m})
}

func (ws *webServer) handleStats(w http.ResponseWriter, r *http.Request) {
	st, err := ws.store.Stats(ws.workspace)
	if err != nil {
		log.Printf("web stats error: %v", err)
		writeJSON(w, http.StatusInternalServerError, map[string]any{"ok": false, "error": "internal error"})
		return
	}
	cats, _ := ws.store.ListCategories("", ws.workspace)
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "data": map[string]any{"stats": st, "categories": cats}})
}

type webAddReq struct {
	Category   string   `json:"category"`
	Type       string   `json:"type"`
	Importance int      `json:"importance"`
	Title      string   `json:"title"`
	Content    string   `json:"content"`
	Problem    string   `json:"problem"`
	Solution   string   `json:"solution"`
	Pitfall    string   `json:"pitfall"`
	Example    string   `json:"example"`
	Scope      string   `json:"scope"`
	Source     string   `json:"source"`
	Confidence int      `json:"confidence"`
	Tags       []string `json:"tags"`
}

func (ws *webServer) handleAdd(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeJSON(w, http.StatusMethodNotAllowed, map[string]any{"ok": false, "error": "method not allowed"})
		return
	}
	var req webAddReq
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]any{"ok": false, "error": "invalid json"})
		return
	}
	if strings.TrimSpace(req.Category) == "" || strings.TrimSpace(req.Content) == "" {
		writeJSON(w, http.StatusBadRequest, map[string]any{"ok": false, "error": "category and content required"})
		return
	}
	if req.Type == "" {
		req.Type = store.TypeNote
	}
	if req.Importance == 0 {
		req.Importance = 3
	}
	if req.Confidence == 0 {
		req.Confidence = 3
	}
	s, ok := ws.store.(interface {
		Create(store.CreateInput) (*store.Memory, error)
	})
	if !ok {
		writeJSON(w, http.StatusInternalServerError, map[string]any{"ok": false, "error": "not supported"})
		return
	}
	m, err := s.Create(store.CreateInput{
		Category:   req.Category,
		Type:       req.Type,
		Importance: req.Importance,
		Title:      req.Title,
		Content:    req.Content,
		Problem:    req.Problem,
		Solution:   req.Solution,
		Pitfall:    req.Pitfall,
		Example:    req.Example,
		Scope:      req.Scope,
		Source:     req.Source,
		Workspace:  ws.workspace,
		Confidence: req.Confidence,
		Tags:       req.Tags,
	})
	if err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]any{"ok": false, "error": err.Error()})
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "data": m})
}

type webUpdateReq struct {
	ID         int64     `json:"id"`
	Category   *string   `json:"category"`
	Type       *string   `json:"type"`
	Importance *int      `json:"importance"`
	Title      *string   `json:"title"`
	Content    *string   `json:"content"`
	Problem    *string   `json:"problem"`
	Solution   *string   `json:"solution"`
	Pitfall    *string   `json:"pitfall"`
	Example    *string   `json:"example"`
	Scope      *string   `json:"scope"`
	Source     *string   `json:"source"`
	Confidence *int      `json:"confidence"`
	SetTags    *[]string `json:"tags"`
}

func (ws *webServer) handleUpdate(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeJSON(w, http.StatusMethodNotAllowed, map[string]any{"ok": false, "error": "method not allowed"})
		return
	}
	var req webUpdateReq
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]any{"ok": false, "error": "invalid json"})
		return
	}
	if req.ID <= 0 {
		writeJSON(w, http.StatusBadRequest, map[string]any{"ok": false, "error": "invalid id"})
		return
	}
	s, ok := ws.store.(interface {
		Update(int64, string, store.UpdateInput) (*store.Memory, error)
	})
	if !ok {
		writeJSON(w, http.StatusInternalServerError, map[string]any{"ok": false, "error": "not supported"})
		return
	}
	in := store.UpdateInput{
		Category:   req.Category,
		Type:       req.Type,
		Importance: req.Importance,
		Title:      req.Title,
		Content:    req.Content,
		Problem:    req.Problem,
		Solution:   req.Solution,
		Pitfall:    req.Pitfall,
		Example:    req.Example,
		Scope:      req.Scope,
		Source:     req.Source,
		Confidence: req.Confidence,
		SetTags:    req.SetTags,
	}
	m, err := s.Update(req.ID, ws.workspace, in)
	if err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]any{"ok": false, "error": err.Error()})
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "data": m})
}

func (ws *webServer) handleDelete(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeJSON(w, http.StatusMethodNotAllowed, map[string]any{"ok": false, "error": "method not allowed"})
		return
	}
	var req struct {
		ID int64 `json:"id"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]any{"ok": false, "error": "invalid json"})
		return
	}
	if req.ID <= 0 {
		writeJSON(w, http.StatusBadRequest, map[string]any{"ok": false, "error": "invalid id"})
		return
	}
	s, ok := ws.store.(interface {
		Delete(int64, string) error
	})
	if !ok {
		writeJSON(w, http.StatusInternalServerError, map[string]any{"ok": false, "error": "not supported"})
		return
	}
	if err := s.Delete(req.ID, ws.workspace); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]any{"ok": false, "error": err.Error()})
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "data": map[string]any{"deleted": req.ID}})
}

func (ws *webServer) handleConflicts(w http.ResponseWriter, r *http.Request) {
	s, ok := ws.store.(interface {
		DetectConflicts(string, int) ([]store.Conflict, error)
	})
	if !ok {
		writeJSON(w, http.StatusInternalServerError, map[string]any{"ok": false, "error": "not supported"})
		return
	}
	conflicts, err := s.DetectConflicts(ws.workspace, 500)
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]any{"ok": false, "error": "internal error"})
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "data": map[string]any{"count": len(conflicts), "conflicts": conflicts}})
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}

func hostCheck(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		h := r.Host
		if h == "" {
			next.ServeHTTP(w, r)
			return
		}
		// allow localhost variants
		if strings.HasPrefix(h, "127.0.0.1:") || h == "127.0.0.1" ||
			strings.HasPrefix(h, "localhost:") || h == "localhost" ||
			strings.HasPrefix(h, "[::1]:") || h == "[::1]" {
			next.ServeHTTP(w, r)
			return
		}
		http.Error(w, "forbidden", http.StatusForbidden)
	})
}

func securityHeaders(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Security-Policy", "default-src 'self'; script-src 'self'; style-src 'self'; img-src 'self' data:; connect-src 'self'")
		w.Header().Set("X-Content-Type-Options", "nosniff")
		w.Header().Set("Referrer-Policy", "no-referrer")
		w.Header().Set("X-Frame-Options", "DENY")
		next.ServeHTTP(w, r)
	})
}

func accessLog(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		start := time.Now()
		rw := &statusWriter{ResponseWriter: w, status: http.StatusOK}
		next.ServeHTTP(rw, r)
		log.Printf("%s %s %d %s", r.Method, r.URL.Path, rw.status, time.Since(start).Truncate(time.Millisecond))
	})
}

type statusWriter struct {
	http.ResponseWriter
	status int
}

func (sw *statusWriter) WriteHeader(code int) {
	sw.status = code
	sw.ResponseWriter.WriteHeader(code)
}

func openBrowser(url string) error {
	var cmd *exec.Cmd
	switch runtime.GOOS {
	case "windows":
		cmd = exec.Command("cmd", "/c", "start", "", url)
		a := sysProcAttr()
		cmd.SysProcAttr = a
	case "darwin":
		cmd = exec.Command("open", url)
	default:
		cmd = exec.Command("xdg-open", url)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	if err := cmd.Start(); err != nil {
		log.Printf("open browser failed: %v", err)
		return nil
	}
	_ = ctx
	return nil
}
