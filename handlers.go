package main

import (
	"database/sql"
	"encoding/json"
	"strconv"
	"time"

	//"database/sql"
	//"github.com/gorilla/websocket"
	"golang.org/x/crypto/bcrypt"
	"log"
	"net/http"
)

// registerHandler обрабатывает запрос на регистрацию
func registerHandler(w http.ResponseWriter, r *http.Request) {
	username := r.FormValue("username")
	password := r.FormValue("password")

	// Логируем полученные данные
	log.Println("Регистрация: username=", username, "password=", password)

	hashedPassword, err := bcrypt.GenerateFromPassword([]byte(password), bcrypt.DefaultCost)
	if err != nil {
		log.Println("Ошибка при хешировании пароля:", err)
		http.Error(w, "Ошибка при хешировании пароля", http.StatusInternalServerError)
		return
	}

	db, err := connectDB()
	if err != nil {
		log.Println("Ошибка подключения к базе данных:", err)
		http.Error(w, "Ошибка подключения к базе данных", http.StatusInternalServerError)
		return
	}
	defer db.Close()

	// Логируем SQL-запрос
	log.Println("Выполнение запроса: INSERT INTO users (username, password) VALUES ($1, $2)", username, string(hashedPassword))

	_, err = db.Exec("INSERT INTO users (username, password) VALUES ($1, $2)", username, string(hashedPassword))
	if err != nil {
		log.Println("Ошибка при регистрации:", err)
		http.Error(w, "Ошибка при регистрации: "+err.Error(), http.StatusInternalServerError)
		return
	}

	w.WriteHeader(http.StatusOK)
	w.Write([]byte("Пользователь зарегистрирован"))
}

func loginHandler(w http.ResponseWriter, r *http.Request) {
	username := r.FormValue("username")
	password := r.FormValue("password")

	db, err := connectDB()
	if err != nil {
		http.Error(w, "Ошибка подключения к базе данных", http.StatusInternalServerError)
		return
	}
	defer db.Close()

	var (
		storedPassword string
		userID         int
	)

	// Добавляем получение ID пользователя
	err = db.QueryRow("SELECT id, password FROM users WHERE username = $1", username).Scan(&userID, &storedPassword)
	if err != nil {
		http.Error(w, "Пользователь не найден", http.StatusUnauthorized)
		return
	}

	err = bcrypt.CompareHashAndPassword([]byte(storedPassword), []byte(password))
	if err != nil {
		http.Error(w, "Неверный пароль", http.StatusUnauthorized)
		return
	}

	// Возвращаем JSON с ID пользователя
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]interface{}{
		"message": "Вход выполнен",
		"userId":  userID,
	})
}
func usersHandler(w http.ResponseWriter, r *http.Request) {
	if r.Method != "GET" {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}

	db, err := connectDB()
	if err != nil {
		http.Error(w, "Database error", http.StatusInternalServerError)
		return
	}
	defer db.Close()

	//rows, err := db.Query("SELECT id, username FROM users")
	currentUserID := r.URL.Query().Get("current_user_id")

	rows, err := db.Query(`
        SELECT u.id, u.username 
        FROM users u
        WHERE u.id != $1 
        AND NOT EXISTS (
            SELECT 1 
            FROM participants p1
            JOIN participants p2 ON p1.chat_id = p2.chat_id 
            WHERE p1.user_id = $1 
            AND p2.user_id = u.id
        )
    `, currentUserID)

	if err != nil {
		http.Error(w, "Query error", http.StatusInternalServerError)
		return
	}
	defer rows.Close()

	var users []map[string]interface{}
	for rows.Next() {
		var id int
		var username string
		if err := rows.Scan(&id, &username); err != nil {
			continue
		}
		users = append(users, map[string]interface{}{"id": id, "username": username})
	}

	json.NewEncoder(w).Encode(users)
}

func chatsHandler(w http.ResponseWriter, r *http.Request) {
	switch r.Method {
	case "GET":
		getChatsHandler(w, r)
	case "POST":
		createChatHandler(w, r)
	default:
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
	}
}

func getChatsHandler(w http.ResponseWriter, r *http.Request) {
	currentUserID := r.URL.Query().Get("user_id")
	if currentUserID == "" {
		http.Error(w, "User ID is required", http.StatusBadRequest)
		return
	}

	db, err := connectDB()
	if err != nil {
		http.Error(w, "Database error", http.StatusInternalServerError)
		return
	}
	defer db.Close()

	rows, err := db.Query(`
        SELECT 
            c.id AS chat_id,
            c.last_message_at,
            p.unread_count,
            m.content AS last_message,
            u.username AS partner_username
        FROM participants p
        JOIN chats c ON p.chat_id = c.id
        LEFT JOIN messages m ON c.last_message_at = m.created_at
        JOIN participants p2 ON p2.chat_id = c.id AND p2.user_id != $1
        JOIN users u ON u.id = p2.user_id
        WHERE p.user_id = $1
        ORDER BY c.last_message_at DESC
    `, currentUserID)
	if err != nil {
		http.Error(w, "Query error: "+err.Error(), http.StatusInternalServerError)
		return
	}
	defer rows.Close()

	var chats []map[string]interface{}
	for rows.Next() {
		var chatID int
		var lastMessage sql.NullString
		var unreadCount int
		var timestamp sql.NullTime
		var partnerUsername string
		if err := rows.Scan(&chatID, &timestamp, &unreadCount, &lastMessage, &partnerUsername); err != nil {
			log.Println("Ошибка сканирования строки:", err)
			continue
		}
		chats = append(chats, map[string]interface{}{
			"id":          chatID,
			"lastMessage": lastMessage.String,
			"unread":      unreadCount,
			"timestamp":   timestamp.Time,
			"username":    partnerUsername, // Имя собеседника
		})
	}

	// Всегда возвращаем JSON, даже если список пуст
	w.Header().Set("Content-Type", "application/json")
	if chats == nil {
		chats = []map[string]interface{}{} // Пустой список
	}
	json.NewEncoder(w).Encode(chats)
}

func createChatHandler(w http.ResponseWriter, r *http.Request) {
	// Получаем ID текущего пользователя из параметров запроса
	currentUserID := r.FormValue("current_user_id")
	targetUserID := r.FormValue("user_id")

	//currentUserID := 1 // TODO: Заменить на реальный ID
	//targetUserID := r.FormValue("user_id")

	db, err := connectDB()
	if err != nil {
		http.Error(w, "Database error", http.StatusInternalServerError)
		return
	}
	defer db.Close()

	tx, err := db.Begin()
	if err != nil {
		http.Error(w, "Transaction error", http.StatusInternalServerError)
		return
	}

	var chatID int
	if err := tx.QueryRow("INSERT INTO chats DEFAULT VALUES RETURNING id").Scan(&chatID); err != nil {
		tx.Rollback()
		http.Error(w, "Chat creation failed", http.StatusInternalServerError)
		return
	}

	if _, err := tx.Exec("INSERT INTO participants (chat_id, user_id) VALUES ($1, $2), ($1, $3)",
		chatID, currentUserID, targetUserID); err != nil {
		tx.Rollback()
		http.Error(w, "Participants error", http.StatusInternalServerError)
		return
	}

	if _, err := tx.Exec("INSERT INTO messages (chat_id, content, is_system) VALUES ($1, $2, true)",
		chatID, "Чат создан"); err != nil {
		tx.Rollback()
		http.Error(w, "System message error", http.StatusInternalServerError)
		return
	}

	tx.Commit()
	json.NewEncoder(w).Encode(map[string]int{"chatId": chatID})
}

func messagesHandler(w http.ResponseWriter, r *http.Request) {
	if r.Method != "GET" {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}

	chatID := r.URL.Query().Get("chat_id")
	currentUserID := r.URL.Query().Get("user_id") // Добавляем ID текущего пользователя
	if chatID == "" || currentUserID == "" {
		http.Error(w, "Chat ID and User ID are required", http.StatusBadRequest)
		return
	}

	db, err := connectDB()
	if err != nil {
		http.Error(w, "Database error", http.StatusInternalServerError)
		return
	}
	defer db.Close()

	rows, err := db.Query(`
        SELECT id, content, created_at, user_id, is_system 
        FROM messages 
        WHERE chat_id = $1 
        ORDER BY created_at DESC
    `, chatID)
	if err != nil {
		http.Error(w, "Query error: "+err.Error(), http.StatusInternalServerError)
		return
	}
	defer rows.Close()

	var messages []map[string]interface{}
	for rows.Next() {
		var id int
		var content string
		var createdAt time.Time
		var userID sql.NullInt64
		var isSystem bool

		if err := rows.Scan(&id, &content, &createdAt, &userID, &isSystem); err != nil {
			log.Println("Error scanning message row:", err)
			continue
		}

		// Вычисляем isMe
		//isMe := userID.Int64 == currentUserID
		// Вычисляем isMe
		userIDInt64 := userID.Int64
		currentUserIDInt64, err := strconv.ParseInt(currentUserID, 10, 64)
		if err != nil {
			log.Println("Error converting currentUserID to int64:", err)
			continue // or handle error differently, e.g., set isMe to false
		}

		isMe := userIDInt64 == currentUserIDInt64

		messages = append(messages, map[string]interface{}{
			"id":         id,
			"text":       content,
			"created_at": createdAt,
			"user_id":    userID.Int64,
			"is_system":  isSystem,
			"isMe":       isMe, // Добавляем isMe
		})
		log.Println(messages)
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(messages)
}
