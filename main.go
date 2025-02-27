package main

import (
	"fmt"
	"log"
	"net/http"
)

// Добавляем middleware для CORS
func enableCORS(next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Access-Control-Allow-Origin", "*")
		w.Header().Set("Access-Control-Allow-Methods", "GET, POST, OPTIONS")
		w.Header().Set("Access-Control-Allow-Headers", "Content-Type, Authorization")
		if r.Method == "OPTIONS" {
			w.WriteHeader(http.StatusOK)
			return
		}
		next.ServeHTTP(w, r)
	}
}

func main() {
	http.HandleFunc("/register", enableCORS(registerHandler))
	http.HandleFunc("/login", enableCORS(loginHandler))
	http.HandleFunc("/users", enableCORS(usersHandler))
	http.HandleFunc("/chats", enableCORS(chatsHandler))
	http.HandleFunc("/ws", enableCORS(handleWebSocket))

	fmt.Println("Server starting on :8080")
	log.Fatal(http.ListenAndServe("0.0.0.0:8080", nil))
}
