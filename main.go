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

type httpError struct {
	Message string `json:"message"`
}

func RootHandler(w http.ResponseWriter, r *http.Request) {

	log.Printf("Received %s %s root request from %s.\n", r.Method, r.URL.Path, r.RemoteAddr)
	w.Header().Set("Content-Type", "application/json")
	if r.Method != http.MethodGet {
		msg := httpError{Message: "Method not allowed"}
		w.WriteHeader(http.StatusBadRequest)
		json.NewEncoder(w).Encode(msg)
		log.Printf("%s: Only GET method is allowed on this api endpoint.\n", r.RemoteAddr)
		return
	}

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
	w.Header().Set("Content-Type", "application/json")
	if r.Method != http.MethodPost {

		msg := httpError{Message: "Method not allowed"}
		w.WriteHeader(http.StatusBadRequest)
		json.NewEncoder(w).Encode(msg)
		log.Printf("%s: Only POST method is allowed on this api endpoint.\n", r.RemoteAddr)
		return

	}

	var request ValidateIPRequest
	err := json.NewDecoder(r.Body).Decode(&request)
	if err != nil {
		msg := httpError{Message: "The requested resource was not a valid json."}
		w.WriteHeader(http.StatusBadRequest)
		json.NewEncoder(w).Encode(msg)
		log.Printf("%s: %s is not a valid json file.\n", request, r.RemoteAddr)
		return
	}

	response := ValidateIPResponse{
		Status: net.ParseIP(request.IP).To4() != nil,
	}
	w.WriteHeader(http.StatusOK)
	json.NewEncoder(w).Encode(response)
	log.Printf("%s: Respond to validate request %s with response %v.\n", r.RemoteAddr, r.URL.Path, response)
}

//lookup Handler definition

type Addr struct {
	Ip net.IP `json:"ip"`
}

type LookupResponse struct {
	Addresses []Addr `json:"addresses"`
	Domain    string `json:"domain"`
	CreatedAt int64  `json:"created_at"`
	Client_ip string `json:"client_ip"`
}

func lookupHandler(db *sql.DB) http.HandlerFunc {

	return func(w http.ResponseWriter, r *http.Request) {
		log.Printf("Received %s %s lookup request from %s.\n", r.Method, r.URL.Path, r.RemoteAddr)
		w.Header().Set("Content-Type", "application/json")

		if r.Method != http.MethodGet {
			msg := httpError{Message: "Method not allowed"}
			w.WriteHeader(http.StatusBadRequest)
			json.NewEncoder(w).Encode(msg)
			log.Printf("%s: Only GET method is allowed on this api endpoint.\n", r.RemoteAddr)
			return
		}

		domain := r.URL.Query().Get("domain")
		if domain == "" {
			msg := httpError{Message: "Bad request: missing domain"}
			w.WriteHeader(http.StatusBadRequest)
			json.NewEncoder(w).Encode(msg)
			log.Printf("%s: please specify correct domain in query string.\n", r.RemoteAddr)
			return
		}

		ips, err := net.DefaultResolver.LookupIP(context.Background(), "ip4", domain)
		if err != nil {
			msg := httpError{Message: "Failed to resolve domain,Please check if the domain is correct."}
			w.WriteHeader(http.StatusNotFound)
			json.NewEncoder(w).Encode(msg)
			log.Printf("%s: Failed to resolve domain %s with error %s\n", domain, r.RemoteAddr, err)
			return
		}
		remoteip, _, _ := net.SplitHostPort(r.RemoteAddr)
		response := LookupResponse{
			Addresses: make([]Addr, 0),
			Domain:    domain,
			CreatedAt: time.Now().Unix(),
			Client_ip: remoteip,
		}

		for _, ip := range ips {
			response.Addresses = append(response.Addresses, Addr{Ip: ip})
		}
		addressJson, err := json.Marshal(response.Addresses)

		log.Printf("%s: Inserting database entry [domain: %s,clientIP: %s,addressJson: %s,createAt: %s].\n", response.Domain, response.Client_ip, response.Addresses, response.CreatedAt)
		_, err = db.Exec(`INSERT INTO query_history (domain, client_ip, addresses, created_at) VALUES ($1, $2, $3, to_timestamp($4))`, response.Domain, response.Client_ip, addressJson, response.CreatedAt)

		if err != nil {

			msg := httpError{Message: "Failed to save query to the database."}
			w.WriteHeader(http.StatusInternalServerError)
			json.NewEncoder(w).Encode(msg)
			log.Printf("%s: Inserting database entry failed with error %s.\n", r.RemoteAddr, err)
			return
		}
		log.Printf("%s: Inserted database entry successfully\n", r.RemoteAddr)

		w.WriteHeader(http.StatusOK)
		json.NewEncoder(w).Encode(response)
		log.Printf("%s: Respond to request %s with lookup response %v.\n", r.RemoteAddr, r.URL.Path, response)
	}
}

func historyHandler(db *sql.DB) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		log.Printf("Received %s %s request from %s.\n", r.Method, r.URL.Path, r.RemoteAddr)

		w.Header().Set("Content-Type", "application/json")
		if r.Method != http.MethodGet {
			msg := httpError{Message: "Method not allowed"}
			w.WriteHeader(http.StatusBadRequest)
			json.NewEncoder(w).Encode(msg)
			log.Printf("%s: Only GET method is allowed on this api endpoint.\n", r.RemoteAddr)
			return
		}

		rows, err := db.Query(`SELECT domain, client_ip, addresses, created_at FROM query_history ORDER BY created_at DESC LIMIT 20`)
		if err != nil {

			msg := httpError{Message: "Failed to fetch query history from database."}
			w.WriteHeader(http.StatusInternalServerError)
			json.NewEncoder(w).Encode(msg)
			log.Printf("%s: fetch query history failed with error %s.\n", r.RemoteAddr, err)
			return
		}
		defer rows.Close()

		QueryHistory := make([]LookupResponse, 0)
		for rows.Next() {
			var qh LookupResponse
			var addressesJSON string
			t := time.Time{}
			err := rows.Scan(&qh.Domain, &qh.Client_ip, &addressesJSON, &t)
			if err != nil {
				msg := httpError{Message: "Failed to decode database row."}
				w.WriteHeader(http.StatusInternalServerError)
				json.NewEncoder(w).Encode(msg)
				log.Printf("%s: Failed to decode database row with error %s.\n", r.RemoteAddr, err)
				return
			}
			qh.CreatedAt = t.Unix()
			json.Unmarshal([]byte(addressesJSON), &qh.Addresses)
			QueryHistory = append(QueryHistory, qh)

		}
		if err = rows.Err(); err != nil {

			msg := httpError{Message: "Failed to fetch query history from database."}
			w.WriteHeader(http.StatusInternalServerError)
			json.NewEncoder(w).Encode(msg)
			log.Printf("%s: fetch query history failed with error %s.\n", r.RemoteAddr, err)
			return
		}

		w.WriteHeader(http.StatusOK)
		json.NewEncoder(w).Encode(QueryHistory)
		log.Printf("%s: Respond to request %s with history: %v.\n", r.RemoteAddr, r.URL.Path, QueryHistory)

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
