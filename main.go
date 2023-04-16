package main

import (
	"context"
	"database/sql"
	_ "embed"
	"encoding/json"
	"fmt"
	"log"
	"net"
	"net/http"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"

	"github.com/prometheus/client_golang/prometheus/promhttp"

	_ "github.com/lib/pq"
)

const version = "0.1.0"

//go:embed swagger.yaml
var swaggerFile string
var err error
var db *sql.DB

//root request and its reponse definition
type rootResponse struct {
	Version    string `json:"version"`
	Date       int64  `json:"date"`
	Kubernetes bool   `json:"kubernetes"`
}

func RootHandler(w http.ResponseWriter, r *http.Request) {

	log.Printf("Received %s %s root request from %s.\n", r.Method, r.URL.Path, r.RemoteAddr)

	w.Header().Set("Content-Type", "application/json")

	response := rootResponse{
		Version:    version,
		Date:       time.Now().Unix(),
		Kubernetes: strings.EqualFold(os.Getenv("KUBERNETES"), "yes"),
	}
	w.WriteHeader(http.StatusOK)
	json.NewEncoder(w).Encode(response)

	log.Printf("Respond to %s root request %s with response %v.\n", r.RemoteAddr, r.URL.Path, response)
}

//valid request and its reponse definition
type ValidateIPRequest struct {
	IP string `json:"ip"`
}

type ValidateIPResponse struct {
	Status bool `json:"status"`
}

func validateIPHandler(w http.ResponseWriter, r *http.Request) {
	log.Printf("Received %s %s validate request from %s.\n", r.Method, r.URL.Path, r.RemoteAddr)
	if r.Method != http.MethodPost {
		http.Error(w, "Method not allowed", http.StatusBadRequest)
		log.Printf("%s: Only POST method is allowed on this api endpoint.\n", r.RemoteAddr)
		return
	}

	var request ValidateIPRequest
	err := json.NewDecoder(r.Body).Decode(&request)
	if err != nil {
		http.Error(w, "Bad request", http.StatusBadRequest)
		log.Printf("%s: %s is not a valid json file.\n", request, r.RemoteAddr)

		return
	}

	response := ValidateIPResponse{
		Status: net.ParseIP(request.IP).To4() != nil,
	}

	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusOK)
	json.NewEncoder(w).Encode(response)
	log.Printf("%s: Respond to validate request %s with response %v.\n", r.RemoteAddr, r.URL.Path, response)
}

//lookup Handler definition

type LookupResponse struct {
	Addresses []net.IP `json:"addresses"`
	Domain    string   `json:"domain"`
}

type QueryHistory struct {
	Domain    string   `json:"domain"`
	ClientIP  string   `json:"client_ip"`
	Addresses []net.IP `json:"addresses"`
	CreatedAt int64    `json:"created_at"`
}

func lookupHandler(db *sql.DB) http.HandlerFunc {

	return func(w http.ResponseWriter, r *http.Request) {
		log.Printf("Received %s %s lookup request from %s.\n", r.Method, r.URL.Path, r.RemoteAddr)

		if r.Method != http.MethodGet {
			http.Error(w, "Method not allowed", http.StatusBadRequest)
			log.Printf("%s: Only GET method is allowed on this api endpoint.\n", r.RemoteAddr)
			return
		}

		domain := r.URL.Query().Get("domain")
		if domain == "" {
			http.Error(w, "Bad request: missing domain", http.StatusBadRequest)
			log.Printf("%s: please specify correct domain in query string.\n", r.RemoteAddr)

			return
		}

		ips, err := net.DefaultResolver.LookupIP(context.Background(), "ip4", domain)
		if err != nil {
			http.Error(w, "Failed to resolve domain,Please check if the domain is correct.", http.StatusNotFound)
			log.Printf("%s: Failed to resolve domain %s with error %s\n", domain, r.RemoteAddr, err)
			return
		}

		response := LookupResponse{
			Addresses: ips,
			Domain:    domain,
		}
		clientIP := r.RemoteAddr
		createdAt := time.Now().Unix()
		addressJson, err := json.Marshal(response.Addresses)
		log.Printf("%s: Inserting database entry [domain: %s,clientIP: %s,addressJson: %s,createaAt: %d].\n", r.RemoteAddr, domain, clientIP, addressJson, createdAt)
		_, err = db.Exec(`INSERT INTO query_history (domain, client_ip, addresses, created_at) VALUES ($1, $2, $3, to_timestamp($4))`, domain, clientIP, addressJson, createdAt)

		if err != nil {
			http.Error(w, "Failed to save query to the database", http.StatusInternalServerError)
			log.Printf("%s: Inserting database entry failed with error %s.\n", r.RemoteAddr, err)
			return
		}
		log.Printf("%s: Inserted database entry successfully\n", r.RemoteAddr)

		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		json.NewEncoder(w).Encode(response)
		log.Printf("%s: Respond to request %s with lookup response %s.\n", r.RemoteAddr, r.URL.Path, response)
	}
}

func historyHandler(db *sql.DB) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		log.Printf("Received %s %s request from %s.\n", r.Method, r.URL.Path, r.RemoteAddr)
		if r.Method != http.MethodGet {
			http.Error(w, "Method not allowed", http.StatusBadRequest)
			log.Printf("%s: Only GET method is allowed on this api endpoint.\n", r.RemoteAddr)
			return
		}

		rows, err := db.Query(`SELECT domain, client_ip, addresses, created_at FROM query_history ORDER BY created_at DESC LIMIT 20`)
		if err != nil {
			http.Error(w, "Failed to fetch query history", http.StatusInternalServerError)
			log.Printf("%s: fetch query history failed with error %s.\n", r.RemoteAddr, err)
			return
		}
		defer rows.Close()

		history := make([]QueryHistory, 0)
		for rows.Next() {
			var qh QueryHistory
			var addressesJSON string
			t := time.Time{}
			err := rows.Scan(&qh.Domain, &qh.ClientIP, &addressesJSON, &t)
			if err != nil {
				http.Error(w, "Failed to decode database row ", http.StatusInternalServerError)
				log.Printf("%s: Failed to decode database row with error %s.\n", r.RemoteAddr, err)
				return
			}
			qh.CreatedAt = t.Unix()
			json.Unmarshal([]byte(addressesJSON), &qh.Addresses)
			history = append(history, qh)

		}
		if err = rows.Err(); err != nil {
			http.Error(w, "Failed to fetch query history", http.StatusInternalServerError)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		json.NewEncoder(w).Encode(history)
		log.Printf("%s: Respond to request %s with history: %v.\n", r.RemoteAddr, r.URL.Path, history)

	}
}

//health check
func healthHandler(w http.ResponseWriter, r *http.Request) {
	log.Printf("Received %s %s request from %s.\n", r.Method, r.URL.Path, r.RemoteAddr)
	w.WriteHeader(http.StatusOK)
	w.Write([]byte("OK"))
	log.Printf("%s: Respond to request %s with response: OK.\n", r.RemoteAddr, r.URL.Path)
}

func swaggerHandler(file string) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		log.Printf("Received %s %s request from %s.\n", r.Method, r.URL.Path, r.RemoteAddr)
		w.Header().Set("Content-Type", "text/yaml")
		fmt.Fprint(w, file)
		log.Printf("%s: Respond to request %s with response: swaggerfile.\n", r.RemoteAddr, r.URL.Path)
	}

}

func metricHandler(metric http.Handler) http.HandlerFunc {

	return func(w http.ResponseWriter, r *http.Request) {
		log.Printf("Received %s %s request from %s.\n", r.Method, r.URL.Path, r.RemoteAddr)
		metric.ServeHTTP(w, r)
		log.Printf("%s: Respond to request %s with grafana metrics.\n", r.RemoteAddr, r.URL.Path)
	}

}

func main() {

	f, err := os.OpenFile("access.log", os.O_RDWR|os.O_CREATE|os.O_APPEND, 0666)
	if err != nil {
		log.Fatalf("error opening log file: %v\n", err)
	}
	defer f.Close()
	log.SetOutput(f)
	log.SetFlags(log.LstdFlags | log.Lmicroseconds | log.LUTC | log.Lshortfile)

	dbHost := os.Getenv("DB_HOST")
	dbPort := os.Getenv("DB_PORT")
	dbName := os.Getenv("DB_NAME")
	dbUser := os.Getenv("DB_USER")
	dbPassword := os.Getenv("DB_PASSWORD")

	dbConStr := fmt.Sprintf("host=%s port=%s dbname=%s user=%s password=%s sslmode=disable", dbHost, dbPort, dbName, dbUser, dbPassword)
	log.Println("Establishing database connection.")

	for i := 1; i <= 5; i++ {
		db, err = sql.Open("postgres", dbConStr)
		if err != nil {
			log.Printf("Error opening database (attempt %d/%d): %v", i, 5, err)
		} else {
			err = db.Ping()
			if err == nil {
				log.Println("Established database connection.")
				break
			}
			log.Printf("Error connecting to database (attempt %d/%d): %v", i, 5, err)
		}
		time.Sleep(time.Duration(i*i) * time.Second)
	}
	if err != nil {
		log.Fatalf("Failed to connect to database after 5 attempts: %v\n", err)
	}

	router := http.NewServeMux()
	router.HandleFunc("/", RootHandler)
	router.HandleFunc("/v1/tools/validate", validateIPHandler)
	router.HandleFunc("/v1/tools/lookup", lookupHandler(db))
	router.HandleFunc("/v1/history", historyHandler(db))
	router.HandleFunc("/health", healthHandler)
	router.HandleFunc("/metrics", metricHandler(promhttp.Handler()))
	router.HandleFunc("/swagger.yaml", swaggerHandler(swaggerFile))
	server := &http.Server{
		Addr:    ":3000",
		Handler: router,
	}

	//graceful shutdown
	go func() {
		if err := server.ListenAndServe(); err != nil && err != http.ErrServerClosed {
			log.Fatalf("Server error: %v\n", err)
		} else {

			log.Println("Api Server started")
		}
	}()

	quit := make(chan os.Signal, 1)
	signal.Notify(quit, syscall.SIGINT, syscall.SIGTERM)
	<-quit

	log.Println("Server received a shutdown signal ,graceful shutting down")
	shutdownCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	if err := server.Shutdown(shutdownCtx); err != nil {
		log.Printf("Server forced to shutdown: %v\n", err)
	}
}
