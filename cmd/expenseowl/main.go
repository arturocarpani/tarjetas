package main

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"flag"
	"fmt"
	"log"
	"net/http"
	"os"
	"path/filepath"
	"time"

	"github.com/tanq16/expenseowl/internal/ai"
	"github.com/tanq16/expenseowl/internal/api"
	"github.com/tanq16/expenseowl/internal/auth"
	"github.com/tanq16/expenseowl/internal/storage"
	"github.com/tanq16/expenseowl/internal/telegram"
	"github.com/tanq16/expenseowl/internal/web"
)

var version = "dev"

func runServer(port int) {
	store, err := storage.InitializeStorage()
	if err != nil {
		log.Fatalf("Failed to initialize storage: %v", err)
	}
	defer store.Close()

	authMgr := auth.NewManager(loadOrCreateSecret(), store)
	if os.Getenv("COOKIE_SECURE") == "true" {
		authMgr.Secure = true
	}
	scanner := ai.NewScanner()
	if scanner.Enabled() {
		log.Println("AI receipt scanning enabled (model:", scanner.Model, ")")
	} else {
		log.Println("AI receipt scanning disabled (set ANTHROPIC_API_KEY to enable)")
	}
	handler := api.NewHandler(store, authMgr, scanner)

	// Telegram bot (optional): enabled when TELEGRAM_BOT_TOKEN is set.
	if token := os.Getenv("TELEGRAM_BOT_TOKEN"); token != "" {
		links := telegram.NewLinkStore()
		bot := telegram.New(token, store, scanner, links)
		handler.SetTelegram(bot, links)
		bot.Start(context.Background())
	} else {
		log.Println("Telegram bot disabled (set TELEGRAM_BOT_TOKEN to enable)")
	}

	// ---- public mux: reachable without a session -------------------------
	public := http.NewServeMux()
	public.HandleFunc("/login", handler.ServeLogin)
	public.HandleFunc("/auth/status", handler.AuthStatus)
	public.HandleFunc("/auth/setup", handler.Setup)
	public.HandleFunc("/auth/login", handler.Login)
	public.HandleFunc("/version", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/plain")
		_, _ = w.Write([]byte(version))
	})
	public.HandleFunc("/healthz", func(w http.ResponseWriter, r *http.Request) {
		if _, err := store.CountUsers(); err != nil {
			writePlain(w, http.StatusServiceUnavailable, "unhealthy")
			return
		}
		writePlain(w, http.StatusOK, "ok")
	})
	// Static assets must be public so the login page can load them.
	for _, p := range []string{
		"/functions.js", "/manifest.json", "/sw.js", "/style.css",
		"/favicon.ico", "/chart.min.js", "/fa.min.css", "/pwa/", "/webfonts/",
	} {
		public.HandleFunc(p, handler.ServeStaticFile)
	}

	// ---- protected mux: requires a valid session -------------------------
	protected := http.NewServeMux()
	protected.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/" {
			http.NotFound(w, r)
			return
		}
		w.Header().Set("Content-Type", "text/html")
		if err := web.ServeTemplate(w, "index.html"); err != nil {
			http.Error(w, "Failed to serve template", http.StatusInternalServerError)
		}
	})
	protected.HandleFunc("/table", handler.ServeTableView)
	protected.HandleFunc("/settings", handler.ServeSettingsPage)

	// session / user management
	protected.HandleFunc("/auth/logout", handler.Logout)
	protected.HandleFunc("/auth/me", handler.Me)
	protected.HandleFunc("/users", handler.Users)
	protected.HandleFunc("/users/delete", handler.DeleteUser)
	protected.HandleFunc("/users/password", handler.ResetPassword)
	protected.HandleFunc("/telegram/link", handler.TelegramLink)

	// config
	protected.HandleFunc("/config", handler.GetConfig)
	protected.HandleFunc("/categories", handler.GetCategories)
	protected.HandleFunc("/categories/edit", handler.UpdateCategories)
	protected.HandleFunc("/cards", handler.GetCards)
	protected.HandleFunc("/cards/edit", handler.UpdateCards)
	protected.HandleFunc("/currency", handler.GetCurrency)
	protected.HandleFunc("/currency/edit", handler.UpdateCurrency)
	protected.HandleFunc("/startdate", handler.GetStartDate)
	protected.HandleFunc("/startdate/edit", handler.UpdateStartDate)

	// expenses
	protected.HandleFunc("/expense", handler.AddExpense)
	protected.HandleFunc("/expense/scan", handler.ScanReceipt)
	protected.HandleFunc("/expenses", handler.GetExpenses)
	protected.HandleFunc("/expense/edit", handler.EditExpense)
	protected.HandleFunc("/expense/delete", handler.DeleteExpense)
	protected.HandleFunc("/expenses/delete", handler.DeleteMultipleExpenses)

	// recurring
	protected.HandleFunc("/recurring-expense", handler.AddRecurringExpense)
	protected.HandleFunc("/recurring-expenses", handler.GetRecurringExpenses)
	protected.HandleFunc("/recurring-expense/edit", handler.UpdateRecurringExpense)
	protected.HandleFunc("/recurring-expense/delete", handler.DeleteRecurringExpense)

	// import/export
	protected.HandleFunc("/export/csv", handler.ExportCSV)
	protected.HandleFunc("/import/csv", handler.ImportCSV)
	protected.HandleFunc("/import/csvold", handler.ImportOldCSV)

	// Everything not matched by a public route falls through to the
	// auth-guarded protected mux.
	public.Handle("/", authMgr.RequireAuth(protected))

	srv := &http.Server{
		Addr:              fmt.Sprintf(":%d", port),
		Handler:           securityHeaders(public),
		ReadHeaderTimeout: 10 * time.Second,
		ReadTimeout:       30 * time.Second,
		WriteTimeout:      60 * time.Second,
		IdleTimeout:       120 * time.Second,
	}
	log.Println("Starting server on port", port, "...")
	if err := srv.ListenAndServe(); err != nil {
		log.Fatalf("Server failed to start: %v", err)
	}
}

// securityHeaders applies a conservative baseline to every response.
func securityHeaders(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		h := w.Header()
		h.Set("X-Content-Type-Options", "nosniff")
		h.Set("X-Frame-Options", "DENY")
		h.Set("Referrer-Policy", "same-origin")
		// All assets are first-party/self-hosted; allow inline styles/scripts the
		// templates rely on. 'self' blocks third-party script/connect exfiltration.
		h.Set("Content-Security-Policy",
			"default-src 'self'; img-src 'self' data:; style-src 'self' 'unsafe-inline'; script-src 'self' 'unsafe-inline'; connect-src 'self'; frame-ancestors 'none'")
		next.ServeHTTP(w, r)
	})
}

func writePlain(w http.ResponseWriter, status int, msg string) {
	w.Header().Set("Content-Type", "text/plain")
	w.WriteHeader(status)
	_, _ = w.Write([]byte(msg))
}

// loadOrCreateSecret returns the HMAC secret for session signing. Order:
// SESSION_SECRET env var, else a persisted random key at SESSION_SECRET_FILE
// (default data/session.key), created on first run.
func loadOrCreateSecret() []byte {
	if s := os.Getenv("SESSION_SECRET"); s != "" {
		return []byte(s)
	}
	path := os.Getenv("SESSION_SECRET_FILE")
	if path == "" {
		path = filepath.Join("data", "session.key")
	}
	if b, err := os.ReadFile(path); err == nil && len(b) > 0 {
		return b
	}
	buf := make([]byte, 32)
	if _, err := rand.Read(buf); err != nil {
		log.Fatalf("Failed to generate session secret: %v", err)
	}
	secret := []byte(hex.EncodeToString(buf))
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err == nil {
		if err := os.WriteFile(path, secret, 0o600); err != nil {
			log.Printf("WARNING: could not persist session secret to %s (%v); sessions will reset on restart", path, err)
		}
	}
	return secret
}

func main() {
	port := flag.Int("port", 8080, "Port to serve from")
	showVersion := flag.Bool("version", false, "Print version and exit")
	flag.Parse()
	if *showVersion {
		fmt.Println(version)
		return
	}
	runServer(*port)
}
