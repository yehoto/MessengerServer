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
	http.HandleFunc("/messages", enableCORS(messagesHandler))
	http.HandleFunc("/ws", enableCORS(handleWebSocket))
	http.HandleFunc("/user/image", enableCORS(userImageHandler))
	http.HandleFunc("/add-reaction", enableCORS(addReactionHandler))
	http.HandleFunc("/get-reactions", enableCORS(getReactionsHandler))
	http.HandleFunc("/uploadFile", enableCORS(uploadFileHandler))
	http.HandleFunc("/user-status", enableCORS(getUserStatusHandler))
	http.HandleFunc("/user/profile", enableCORS(userProfileHandler))
	http.HandleFunc("/group-chats", enableCORS(createGroupChatHandler))
	http.HandleFunc("/all-users", enableCORS(allUsersHandler))

	fmt.Println("Server starting on :8080")
	log.Fatal(http.ListenAndServe("0.0.0.0:8080", nil))
}
