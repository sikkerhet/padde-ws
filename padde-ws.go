package main

import (
	"embed"
	"encoding/json"
	"flag"
	"log"
	"net/http"
	"strconv"
	"strings"

	_ "github.com/ClickHouse/clickhouse-go"
	"github.com/gorilla/mux"
	"github.com/jmoiron/sqlx"
)

type SearchRequest struct {
	Query string `json:"query"`
}

type SearchResponse struct {
	Data []Record `json:"data"`
}

type Record struct {
	Query  string `db:"query" json:"query"`
	Answer string `db:"answer" json:"answer"`
	QType  string `db:"qtype" json:"qtype"`
	First  uint64 `db:"first" json:"first"`
	Last   uint64 `db:"last" json:"last"`
	Count  uint64 `db:"count" json:"count"`
}

type ErrorResponse struct {
	Message string `json:"message"`
}

var db *sqlx.DB

//go:embed index.html logo.png
var indexHTML embed.FS

func main() {
	var err error
	db, err = sqlx.Connect("clickhouse", "tcp://localhost:9000?database=padde&user=default")
	if err != nil {
		log.Fatalf("Failed to connect to the ClickHouse database: %v", err)
	}

	var httpHost, httpPort string
	flag.StringVar(&httpHost, "http_host", "0.0.0.0", "HTTP server host")
	flag.StringVar(&httpPort, "http_port", "8080", "HTTP server port")
	flag.Parse()

	router := mux.NewRouter().StrictSlash(true)
	router.Use(loggingMiddleware)
	router.HandleFunc("/search", searchHandler).Methods("GET")
	router.HandleFunc("/", indexHandler).Methods("GET")
	router.HandleFunc("/logo", logoHandler).Methods("GET")

	log.Fatal(http.ListenAndServe(httpHost+":"+httpPort, router))
}

func loggingMiddleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		log.Printf("Request: %s %s", r.Method, r.URL)
		next.ServeHTTP(w, r)
	})
}

func respondWithError(w http.ResponseWriter, status int, message string) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	json.NewEncoder(w).Encode(ErrorResponse{Message: message})
}

func indexHandler(w http.ResponseWriter, r *http.Request) {
	data, err := indexHTML.ReadFile("index.html")
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	w.Write(data)
}

func logoHandler(w http.ResponseWriter, r *http.Request) {
	data, err := indexHTML.ReadFile("logo.png")
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	w.Write(data)
}

func searchHandler(w http.ResponseWriter, r *http.Request) {
	queryQuery := r.URL.Query().Get("q")
	answerQuery := r.URL.Query().Get("a")
	typeList := r.URL.Query().Get("type")
	startTime := r.URL.Query().Get("start")
	stopTime := r.URL.Query().Get("stop")
	max := r.URL.Query().Get("max")
	ilike := r.URL.Query().Get("ilike")

	if max == "" {
		max = "20"
	}

	if queryQuery == "" && answerQuery == "" {
		respondWithError(w, http.StatusBadRequest, "At least one of 'a' or 'q' parameters is required")
		return
	}

	query := `
	SELECT *
	FROM padde.log final
	WHERE (query like ? or answer like ?)
	`
	args := []interface{}{queryQuery, answerQuery}

	if queryQuery == "" {
		query = `
		SELECT *
		FROM padde.log final
		WHERE (answer like ?)
	`
		args = []interface{}{answerQuery}
	}

	if answerQuery == "" {
		query = `
		SELECT *
		FROM padde.log final 
		WHERE (query like ?)
	`
		args = []interface{}{queryQuery}
	}

	if typeList != "" {
		typeListArray := strings.Split(typeList, ",")
		placeholders := make([]string, len(typeListArray))

		for i := range typeListArray {
			placeholders[i] = "?"
		}

		query += " AND qtype IN (" + strings.Join(placeholders, ",") + ")"
		for _, t := range typeListArray {
			args = append(args, t)
		}
	}

	if startTime != "" {
		query += " AND first >= ?"
		args = append(args, startTime)
	}

	if stopTime != "" {
		query += " AND last <= ?"
		args = append(args, stopTime)
	}

	query += " LIMIT ?"
	limit, err := strconv.Atoi(max)
	if err != nil {
		respondWithError(w, http.StatusBadRequest, "Max value not integer")
		return
	}
	if limit > 2000 {
		limit = 2000
	}
	args = append(args, limit)

	if !strings.Contains(queryQuery, "%") && !strings.Contains(answerQuery, "%") {
		query = strings.ReplaceAll(query, "like", "=")
	}

	if ilike != "" {
		query = strings.ReplaceAll(query, "like", "ilike")
	}

	var records []Record
	err = db.Select(&records, query, args...)
	if err != nil {
		log.Printf("Error querying ClickHouse: %v", err)
		respondWithError(w, http.StatusInternalServerError, "Error querying ClickHouse")
		return
	}

	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusOK)
	json.NewEncoder(w).Encode(SearchResponse{Data: records})
}
