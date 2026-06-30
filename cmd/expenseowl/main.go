package main

import (
	"flag"
	"fmt"
	"log"
	"net/http"
	"os"

	"github.com/tanq16/expenseowl/internal/api"
	"github.com/tanq16/expenseowl/internal/auth"
	"github.com/tanq16/expenseowl/internal/storage"
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
		password = "admin"
		log.Println("WARNING: no ADMIN_PASSWORD set, using default 'admin' — change it immediately via Settings")
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

func runServer(port int) {
	store, err := storage.InitializeStorage()
	if err != nil {
		log.Fatalf("Failed to initialize storage: %v", err)
	}
	defer store.Close()
	bootstrapAdmin(store)

	sessions := auth.NewSessionStore()
	handler := api.NewHandler(store, sessions)

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
