package main

import (
	"flag"
	"fmt"
	"log"
	"net/http"
	"os"
	"path/filepath"
	"strings"

	"github.com/tanq16/expenseowl/internal/ai"
	"github.com/tanq16/expenseowl/internal/api"
	"github.com/tanq16/expenseowl/internal/auth"
	"github.com/tanq16/expenseowl/internal/storage"
	"github.com/tanq16/expenseowl/internal/telegram"
	"github.com/tanq16/expenseowl/internal/web"
)

var version = "dev"

// bootstrapAdmin creates the initial admin account if no users exist yet.
func bootstrapAdmin(store storage.Storage) {
	count, err := store.CountUsers()
	if err != nil {
		log.Fatalf("Failed to count users: %v", err)
	}
	if count > 0 {
		return
	}
	username := os.Getenv("ADMIN_USERNAME")
	if username == "" {
		username = "admin"
	}
	password := os.Getenv("ADMIN_PASSWORD")
	if password == "" {
		log.Fatal("No users exist and ADMIN_PASSWORD is not set. Set ADMIN_PASSWORD (and optionally ADMIN_USERNAME) to create the initial admin, then restart.")
	}
	hash, err := auth.HashPassword(password)
	if err != nil {
		log.Fatalf("Failed to hash admin password: %v", err)
	}
	if err := store.CreateUser(storage.User{Username: username, PasswordHash: hash, IsAdmin: true}); err != nil {
		log.Fatalf("Failed to create initial admin user: %v", err)
	}
	log.Printf("Created initial admin user '%s'", username)
}

// receiptsDir returns the directory where receipt images are stored, alongside
// the JSON data dir so it lands on the same persistent volume. For Postgres the
// STORAGE_URL is a DB URL, so fall back to the default "data" dir.
func receiptsDir() string {
	dataDir := os.Getenv("STORAGE_URL")
	if dataDir == "" || strings.EqualFold(os.Getenv("STORAGE_TYPE"), "postgres") {
		dataDir = "data"
	}
	return filepath.Join(dataDir, "receipts")
}

// setupTelegram enables the Telegram bot if its env vars are present and
// registers the webhook with Telegram using the public Railway domain. When
// credentials are missing, the bot is simply disabled and the web app runs
// normally.
func setupTelegram(handler *api.Handler) {
	token := os.Getenv("TELEGRAM_BOT_TOKEN")
	apiKey := os.Getenv("ANTHROPIC_API_KEY")
	if token == "" || apiKey == "" {
		log.Println("Telegram bot disabled (set TELEGRAM_BOT_TOKEN and ANTHROPIC_API_KEY to enable)")
		return
	}
	// The webhook route is public, so a secret is mandatory — without it anyone
	// could POST forged expense updates. Refuse to enable the bot without one.
	secret := os.Getenv("TELEGRAM_WEBHOOK_SECRET")
	if secret == "" {
		log.Println("Telegram bot disabled: set TELEGRAM_WEBHOOK_SECRET (required to secure the public webhook)")
		return
	}
	client := telegram.NewClient(token)
	handler.EnableTelegram(client, ai.NewExtractor(), secret)
	log.Println("Telegram bot enabled")

	domain := os.Getenv("RAILWAY_PUBLIC_DOMAIN")
	if domain == "" {
		log.Println("RAILWAY_PUBLIC_DOMAIN not set — skipping automatic setWebhook; register the webhook manually")
		return
	}
	webhookURL := fmt.Sprintf("https://%s/telegram/webhook", domain)
	if err := client.SetWebhook(webhookURL, secret); err != nil {
		log.Printf("Failed to register Telegram webhook: %v", err)
		return
	}
	log.Printf("Registered Telegram webhook at %s", webhookURL)
}

func runServer(port int) {
	store, err := storage.InitializeStorage()
	if err != nil {
		log.Fatalf("Failed to initialize storage: %v", err)
	}
	defer store.Close()
	bootstrapAdmin(store)

	sessions := auth.NewSessionStore()
	handler := api.NewHandler(store, sessions)
	handler.SetReceiptsDir(receiptsDir())
	setupTelegram(handler)

	// Version Handler (public)
	http.HandleFunc("/version", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
			return
		}
		w.Header().Set("Content-Type", "text/plain")
		w.Write([]byte(version))
	})

	// Auth (public)
	http.HandleFunc("/login", func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodPost {
			handler.Login(w, r)
			return
		}
		handler.ServeLoginPage(w, r)
	})
	http.HandleFunc("/logout", handler.Logout)

	// UI Handlers (protected — redirect to /login)
	http.HandleFunc("/", handler.RequirePage(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/" {
			http.NotFound(w, r)
			return
		}
		w.Header().Set("Content-Type", "text/html")
		if err := web.ServeTemplate(w, "index.html"); err != nil {
			log.Printf("HTTP ERROR: Failed to serve template: %v", err)
			http.Error(w, "Failed to serve template", http.StatusInternalServerError)
			return
		}
	}))
	http.HandleFunc("/table", handler.RequirePage(handler.ServeTableView))
	http.HandleFunc("/settings", handler.RequirePage(handler.ServeSettingsPage))

	// Static File Handlers (public — needed to render the login page)
	http.HandleFunc("/functions.js", handler.ServeStaticFile)
	http.HandleFunc("/manifest.json", handler.ServeStaticFile)
	http.HandleFunc("/sw.js", handler.ServeStaticFile)
	http.HandleFunc("/pwa/", handler.ServeStaticFile)
	http.HandleFunc("/style.css", handler.ServeStaticFile)
	http.HandleFunc("/favicon.ico", handler.ServeStaticFile)
	http.HandleFunc("/chart.min.js", handler.ServeStaticFile)
	http.HandleFunc("/fa.min.css", handler.ServeStaticFile)
	http.HandleFunc("/webfonts/", handler.ServeStaticFile)

	// Current user (protected)
	http.HandleFunc("/me", handler.RequireAPI(handler.Me))

	// User management (protected; admin enforced inside the handlers)
	http.HandleFunc("/users", handler.RequireAPI(handler.Users))
	http.HandleFunc("/users/delete", handler.RequireAPI(handler.DeleteUser))
	http.HandleFunc("/users/password", handler.RequireAPI(handler.UpdateUserPassword))
	http.HandleFunc("/users/telegram", handler.RequireAPI(handler.UpdateUserTelegramID))

	// Telegram webhook (public — authenticated via the secret-token header)
	if handler.TelegramEnabled() {
		http.HandleFunc("/telegram/webhook", handler.TelegramWebhook)
	}

	// Config (protected; PUT endpoints enforce admin inside the handlers)
	http.HandleFunc("/config", handler.RequireAPI(handler.GetConfig))
	http.HandleFunc("/categories", handler.RequireAPI(handler.GetCategories))
	http.HandleFunc("/categories/edit", handler.RequireAPI(handler.UpdateCategories))
	http.HandleFunc("/cards", handler.RequireAPI(handler.GetCards))
	http.HandleFunc("/cards/edit", handler.RequireAPI(handler.UpdateCards))
	http.HandleFunc("/currency", handler.RequireAPI(handler.GetCurrency))
	http.HandleFunc("/currency/edit", handler.RequireAPI(handler.UpdateCurrency))
	http.HandleFunc("/startdate", handler.RequireAPI(handler.GetStartDate))
	http.HandleFunc("/startdate/edit", handler.RequireAPI(handler.UpdateStartDate))

	// Expenses (protected)
	http.HandleFunc("/expense", handler.RequireAPI(handler.AddExpense))                     // PUT for add
	http.HandleFunc("/expenses", handler.RequireAPI(handler.GetExpenses))                   // GET all
	http.HandleFunc("/expense/edit", handler.RequireAPI(handler.EditExpense))               // PUT for edit
	http.HandleFunc("/expense/delete", handler.RequireAPI(handler.DeleteExpense))           // DELETE for single
	http.HandleFunc("/expenses/delete", handler.RequireAPI(handler.DeleteMultipleExpenses)) // DELETE for multiple
	http.HandleFunc("/receipt", handler.RequireAPI(handler.ServeReceipt))                   // GET receipt image

	// Recurring Expenses (protected)
	http.HandleFunc("/recurring-expense", handler.RequireAPI(handler.AddRecurringExpense))
	http.HandleFunc("/recurring-expenses", handler.RequireAPI(handler.GetRecurringExpenses))
	http.HandleFunc("/recurring-expense/edit", handler.RequireAPI(handler.UpdateRecurringExpense))
	http.HandleFunc("/recurring-expense/delete", handler.RequireAPI(handler.DeleteRecurringExpense))

	// Import/Export (protected)
	http.HandleFunc("/export/csv", handler.RequireAPI(handler.ExportCSV))
	http.HandleFunc("/import/csv", handler.RequireAPI(handler.ImportCSV))
	http.HandleFunc("/import/csvold", handler.RequireAPI(handler.ImportOldCSV))

	log.Println("Starting server on port", port, "...")
	if err := http.ListenAndServe(fmt.Sprint(":", port), nil); err != nil {
		log.Fatalf("Server failed to start: %v", err)
	}
}

func main() {
	port := flag.Int("port", 8080, "Port to serve from")
	flag.Parse()
	runServer(*port)
}
