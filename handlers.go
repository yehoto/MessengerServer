package main

import (
	"database/sql"
	"encoding/json"
	"io"
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
	// Ограничиваем размер файла до 5MB
	r.ParseMultipartForm(5 << 20)

	username := r.FormValue("username")
	password := r.FormValue("password")
	name := r.FormValue("name")
	bio := r.FormValue("bio")

	// Обработка файла изображения
	var imageBytes []byte
	file, _, err := r.FormFile("image")
	if err == nil {
		defer file.Close()
		imageBytes, err = io.ReadAll(file)
		if err != nil {
			http.Error(w, "Error reading image", http.StatusBadRequest)
			return
		}
	}

	// Валидация обязательных полей
	if username == "" || name == "" {
		http.Error(w, "Username and name are required", http.StatusBadRequest)
		return
	}

	hashedPassword, err := bcrypt.GenerateFromPassword([]byte(password), bcrypt.DefaultCost)
	if err != nil {
		http.Error(w, "Password hashing failed", http.StatusInternalServerError)
		return
	}

	db, err := connectDB()
	if err != nil {
		http.Error(w, "Database connection failed", http.StatusInternalServerError)
		return
	}
	defer db.Close()

	// Сохраняем пользователя в базе данных
	var image interface{}
	if len(imageBytes) > 0 {
		image = imageBytes
	} else {
		image = nil // Сохраняем NULL, если фото нет
	}

	_, err = db.Exec(
		"INSERT INTO users (username, password, name, bio, image) VALUES ($1, $2, $3, $4, $5)",
		username,
		string(hashedPassword),
		name,
		bio,
		image,
	)

	if err != nil {
		http.Error(w, "Registration failed: "+err.Error(), http.StatusInternalServerError)
		return
	}

	w.WriteHeader(http.StatusOK)
	w.Write([]byte("User registered successfully"))
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
            u.name AS partner_name,
            u.id AS partner_id
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
		var (
			chatID      int
			timestamp   sql.NullTime
			unreadCount int
			lastMessage sql.NullString
			partnerName string
			partnerID   int
		)

		// Порядок должен точно соответствовать SELECT
		if err := rows.Scan(
			&chatID,
			&timestamp,
			&unreadCount,
			&lastMessage,
			&partnerName,
			&partnerID,
		); err != nil {
			log.Println("Ошибка сканирования строки:", err)
			continue
		}

		chats = append(chats, map[string]interface{}{
			"id":           chatID,
			"lastMessage":  lastMessage.String,
			"unread":       unreadCount,
			"timestamp":    timestamp.Time,
			"partner_name": partnerName,
			"partner_id":   partnerID,
		})
	}

	w.Header().Set("Content-Type", "application/json")
	if chats == nil {
		chats = []map[string]interface{}{}
	}
	json.NewEncoder(w).Encode(chats)
}

func userImageHandler(w http.ResponseWriter, r *http.Request) {
	userID := r.URL.Query().Get("id")

	db, err := connectDB()
	if err != nil {
		http.Error(w, "Database error", http.StatusInternalServerError)
		return
	}
	defer db.Close()

	var imageBytes []byte
	err = db.QueryRow("SELECT image FROM users WHERE id = $1", userID).Scan(&imageBytes)
	if err != nil || len(imageBytes) == 0 {
		w.WriteHeader(http.StatusNoContent) // Возвращаем 204, если фото нет
		return
	}

	w.Header().Set("Content-Type", "image/jpeg")
	w.Write(imageBytes)
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
        ORDER BY created_at ASC
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
		//log.Println(messages)
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(messages)
}

func addReactionHandler(w http.ResponseWriter, r *http.Request) {
	var data struct {
		MessageID int    `json:"message_id"`
		UserID    int    `json:"user_id"`
		Reaction  string `json:"reaction"`
	}

	if err := json.NewDecoder(r.Body).Decode(&data); err != nil {
		http.Error(w, "Invalid request body", http.StatusBadRequest)
		return
	}

	db, err := connectDB()
	if err != nil {
		http.Error(w, "Database error", http.StatusInternalServerError)
		return
	}
	defer db.Close()

	_, err = db.Exec(
		"INSERT INTO message_reactions (message_id, user_id, reaction) VALUES ($1, $2, $3) ON CONFLICT (message_id, user_id) DO UPDATE SET reaction = $3",
		data.MessageID,
		data.UserID,
		data.Reaction,
	)
	if err != nil {
		http.Error(w, "Failed to add reaction", http.StatusInternalServerError)
		return
	}

	// Отправляем реакцию всем подключенным клиентам
	reactionMessage := map[string]interface{}{
		"type":       "reaction",
		"message_id": data.MessageID,
		"user_id":    data.UserID,
		"reaction":   data.Reaction,
	}

	clientsMu.Lock()
	for client := range clients {
		err := client.WriteJSON(reactionMessage)
		if err != nil {
			log.Println("Ошибка при отправке реакции:", err)
			client.Close()
			delete(clients, client)
		}
	}
	clientsMu.Unlock()

	w.WriteHeader(http.StatusOK)
	w.Write([]byte("Reaction added successfully"))
}

func uploadFileHandler(w http.ResponseWriter, r *http.Request) {
	r.ParseMultipartForm(10 << 20) // 10 MB

	file, handler, err := r.FormFile("file")
	if err != nil {
		http.Error(w, "Unable to retrieve file", http.StatusBadRequest)
		return
	}
	defer file.Close()

	fileBytes, err := io.ReadAll(file)
	if err != nil {
		http.Error(w, "Unable to read file", http.StatusInternalServerError)
		return
	}

	messageID := r.FormValue("message_id")
	if messageID == "" {
		http.Error(w, "Message ID is required", http.StatusBadRequest)
		return
	}

	db, err := connectDB()
	if err != nil {
		http.Error(w, "Database error", http.StatusInternalServerError)
		return
	}
	defer db.Close()

	_, err = db.Exec(
		"INSERT INTO message_files (message_id, file_name, file_data) VALUES ($1, $2, $3)",
		messageID,
		handler.Filename,
		fileBytes,
	)
	if err != nil {
		http.Error(w, "Failed to upload file", http.StatusInternalServerError)
		return
	}

	w.WriteHeader(http.StatusOK)
	w.Write([]byte("File uploaded successfully"))
}
func getReactionsHandler(w http.ResponseWriter, r *http.Request) {
	messageID := r.URL.Query().Get("message_id")
	if messageID == "" {
		http.Error(w, "Message ID is required", http.StatusBadRequest)
		return
	}

	db, err := connectDB()
	if err != nil {
		log.Println("Database connection error:", err)
		http.Error(w, "Database error", http.StatusInternalServerError)
		return
	}
	defer db.Close()

	rows, err := db.Query("SELECT user_id, reaction FROM message_reactions WHERE message_id = $1", messageID)
	if err != nil {
		log.Println("Query error:", err)
		http.Error(w, "Query error", http.StatusInternalServerError)
		return
	}
	defer rows.Close()

	var reactions []map[string]interface{}
	for rows.Next() {
		var userID int
		var reaction string
		if err := rows.Scan(&userID, &reaction); err != nil {
			log.Println("Row scan error:", err)
			continue
		}
		reactions = append(reactions, map[string]interface{}{"user_id": userID, "reaction": reaction})
	}

	// Если реакций нет, возвращаем пустой список
	if reactions == nil {
		reactions = []map[string]interface{}{}
	}

	w.Header().Set("Content-Type", "application/json")
	if err := json.NewEncoder(w).Encode(reactions); err != nil {
		log.Println("JSON encoding error:", err)
		http.Error(w, "Failed to encode response", http.StatusInternalServerError)
	}
}

func markMessageDelivered(w http.ResponseWriter, r *http.Request) {
	messageID := r.URL.Query().Get("message_id")
	if messageID == "" {
		http.Error(w, "Message ID is required", http.StatusBadRequest)
		return
	}

	db, err := connectDB()
	if err != nil {
		http.Error(w, "Database error", http.StatusInternalServerError)
		return
	}
	defer db.Close()

	_, err = db.Exec("UPDATE messages SET delivered_at = NOW() WHERE id = $1", messageID)
	if err != nil {
		http.Error(w, "Failed to mark message as delivered", http.StatusInternalServerError)
		return
	}

	w.WriteHeader(http.StatusOK)
	w.Write([]byte("Message marked as delivered"))
}

func markMessageRead(w http.ResponseWriter, r *http.Request) {
	messageID := r.URL.Query().Get("message_id")
	if messageID == "" {
		http.Error(w, "Message ID is required", http.StatusBadRequest)
		return
	}

	db, err := connectDB()
	if err != nil {
		http.Error(w, "Database error", http.StatusInternalServerError)
		return
	}
	defer db.Close()

	_, err = db.Exec("UPDATE messages SET read_at = NOW() WHERE id = $1", messageID)
	if err != nil {
		http.Error(w, "Failed to mark message as read", http.StatusInternalServerError)
		return
	}

	w.WriteHeader(http.StatusOK)
	w.Write([]byte("Message marked as read"))
}

// userProfileHandler обрабатывает запрос на получение профиля пользователя
func userProfileHandler(w http.ResponseWriter, r *http.Request) {
	// Получаем ID пользователя из параметров запроса
	userID := r.URL.Query().Get("id")
	if userID == "" {
		http.Error(w, "User ID is required", http.StatusBadRequest)
		return
	}

	db, err := connectDB()
	if err != nil {
		http.Error(w, "Database error", http.StatusInternalServerError)
		return
	}
	defer db.Close()

	// Запрашиваем данные пользователя из базы данных
	var (
		name             string
		username         string
		bio              string
		imageBytes       []byte
		registrationDate time.Time
	)

	err = db.QueryRow(`
		SELECT name, username, bio, image, created_at 
		FROM users 
		WHERE id = $1
	`, userID).Scan(&name, &username, &bio, &imageBytes, &registrationDate)

	if err != nil {
		if err == sql.ErrNoRows {
			http.Error(w, "User not found", http.StatusNotFound)
		} else {
			http.Error(w, "Database query error", http.StatusInternalServerError)
		}
		return
	}

	// Формируем JSON-ответ
	response := map[string]interface{}{
		"name":             name,
		"username":         username,
		"bio":              bio,
		"image":            imageBytes,                            // Возвращаем бинарные данные изображения
		"registrationDate": registrationDate.Format("2006-01-02"), // Форматируем дату
	}
	log.Printf("Зpppp [%s]", response)

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(response)
}
