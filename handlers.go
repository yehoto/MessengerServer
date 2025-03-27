package main

import (
	"database/sql"
	"encoding/json"
	"io"
	"strconv"
	"strings"
	"time"

	//"database/sql"
	//"github.com/gorilla/websocket"
	"log"
	"net/http"

	"golang.org/x/crypto/bcrypt"
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

	// Исправленный SQL-запрос без комментариев и с правильными JOIN
	rows, err := db.Query(`
        SELECT 
            c.id AS chat_id,
            c.last_message_at,
            p.unread_count,
            m.content AS last_message,
            CASE
                WHEN c.is_group THEN gc.name
                ELSE u.name
            END AS chat_name,
            CASE
                WHEN c.is_group THEN NULL
                ELSE u.id
            END AS partner_id,
            c.is_group,
            gc.image as group_image,
            u.name as partner_name
        FROM participants p
        JOIN chats c ON p.chat_id = c.id
        LEFT JOIN (
            SELECT DISTINCT ON (chat_id) chat_id, content 
            FROM messages 
            ORDER BY chat_id, created_at DESC
        ) m ON m.chat_id = c.id
        LEFT JOIN group_chats gc ON gc.chat_id = c.id AND c.is_group
        LEFT JOIN participants p2 ON p2.chat_id = c.id AND p2.user_id != $1 AND NOT c.is_group
        LEFT JOIN users u ON u.id = p2.user_id
        WHERE p.user_id = $1
        ORDER BY c.last_message_at DESC
    `, currentUserID)

	if err != nil {
		log.Printf("Query error: %v", err)
		http.Error(w, "Database query error", http.StatusInternalServerError)
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
			chatName    sql.NullString
			partnerID   sql.NullInt64
			isGroup     bool
			groupImage  []byte
			partnerName sql.NullString
		)

		if err := rows.Scan(
			&chatID,
			&timestamp,
			&unreadCount,
			&lastMessage,
			&chatName,
			&partnerID,
			&isGroup,
			&groupImage,
			&partnerName,
		); err != nil {
			log.Printf("Scan error: %v", err)
			continue
		}

		chatData := map[string]interface{}{
			"id":          chatID,
			"lastMessage": lastMessage.String,
			"unread":      unreadCount,
			"timestamp":   timestamp.Time,
			"chat_name":   chatName.String,
			"is_group":    isGroup,
		}

		if isGroup {
			if len(groupImage) > 0 {
				chatData["group_image"] = groupImage
			}
		} else {
			if partnerID.Valid {
				chatData["partner_id"] = partnerID.Int64
			}
			if partnerName.Valid {
				chatData["partner_name"] = partnerName.String
			}
		}

		chats = append(chats, chatData)
	}

	if err := rows.Err(); err != nil {
		log.Printf("Rows error: %v", err)
		http.Error(w, "Error processing results", http.StatusInternalServerError)
		return
	}

	w.Header().Set("Content-Type", "application/json")
	if err := json.NewEncoder(w).Encode(chats); err != nil {
		log.Printf("JSON encode error: %v", err)
		http.Error(w, "Error encoding response", http.StatusInternalServerError)
	}
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
	currentUserID := r.URL.Query().Get("user_id")
	log.Printf("Запрос сообщений: chat_id=%s, user_id=%s", chatID, currentUserID)

	if chatID == "" || currentUserID == "" {
		log.Printf("Ошибка: отсутствуют chat_id или user_id")
		http.Error(w, "Chat ID and User ID are required", http.StatusBadRequest)
		return
	}

	db, err := connectDB()
	if err != nil {
		log.Printf("Ошибка подключения к БД: %v", err)
		http.Error(w, "Database error", http.StatusInternalServerError)
		return
	}
	defer db.Close()

	var isGroup bool
	err = db.QueryRow("SELECT is_group FROM chats WHERE id = $1", chatID).Scan(&isGroup)
	if err != nil {
		log.Printf("Ошибка получения типа чата: %v", err)
		http.Error(w, "Failed to get chat type", http.StatusInternalServerError)
		return
	}
	log.Printf("Чат групповой: %t", isGroup)

	rows, err := db.Query(`
	  SELECT 
		  m.id, m.content, m.created_at, m.user_id, m.is_system,
		  m.parent_message_id, m.is_forwarded, m.original_sender_id, m.original_chat_id,
		  u.username AS sender_name, pm.content AS parent_content, pu.username AS parent_sender,
		  ou.username AS original_sender_name
	  FROM messages m
	  LEFT JOIN users u ON m.user_id = u.id
	  LEFT JOIN messages pm ON m.parent_message_id = pm.id
	  LEFT JOIN users pu ON pm.user_id = pu.id
	  LEFT JOIN users ou ON m.original_sender_id = ou.id
	  WHERE m.chat_id = $1
	  ORDER BY m.created_at ASC
	`, chatID)
	if err != nil {
		log.Printf("Ошибка выполнения запроса к БД: %v", err)
		http.Error(w, "Query error: "+err.Error(), http.StatusInternalServerError)
		return
	}
	defer rows.Close()

	var messages []map[string]interface{}
	for rows.Next() {
		var (
			id                 int
			content            string
			createdAt          time.Time
			userID             sql.NullInt64
			isSystem           bool
			parentMessageID    sql.NullInt64
			isForwarded        bool
			originalSender     sql.NullInt64
			originalChat       sql.NullInt64
			senderName         sql.NullString
			parentContent      sql.NullString
			parentSender       sql.NullString
			originalSenderName sql.NullString
		)

		if err := rows.Scan(
			&id, &content, &createdAt, &userID, &isSystem,
			&parentMessageID, &isForwarded, &originalSender, &originalChat,
			&senderName, &parentContent, &parentSender, &originalSenderName,
		); err != nil {
			log.Printf("Ошибка чтения строки результата: %v", err)
			continue
		}

		currentUserIDInt, _ := strconv.ParseInt(currentUserID, 10, 64)
		isMe := userID.Int64 == currentUserIDInt

		messageData := map[string]interface{}{
			"id":                   id,
			"text":                 content,
			"created_at":           createdAt,
			"user_id":              userID.Int64,
			"is_system":            isSystem,
			"isMe":                 isMe,
			"is_group":             isGroup,
			"parent_message_id":    parentMessageID.Int64,
			"parent_content":       parentContent.String,
			"parent_sender":        parentSender.String,
			"is_forwarded":         isForwarded,
			"original_sender_id":   originalSender.Int64,
			"original_chat_id":     originalChat.Int64,
			"original_sender_name": originalSenderName.String,
		}
		if senderName.Valid {
			messageData["sender_name"] = senderName.String
		}
		messages = append(messages, messageData)
	}
	log.Printf("Загружено сообщений: %d", len(messages))

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(messages)
}

// func messagesHandler(w http.ResponseWriter, r *http.Request) {
// 	if r.Method != "GET" {
// 		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
// 		return
// 	}

// 	chatID := r.URL.Query().Get("chat_id")
// 	currentUserID := r.URL.Query().Get("user_id")
// 	if chatID == "" || currentUserID == "" {
// 		http.Error(w, "Chat ID and User ID are required", http.StatusBadRequest)
// 		return
// 	}

// 	db, err := connectDB()
// 	if err != nil {
// 		http.Error(w, "Database error", http.StatusInternalServerError)
// 		return
// 	}
// 	defer db.Close()

// 	// Проверяем, является ли чат групповым
// 	var isGroup bool
// 	err = db.QueryRow("SELECT is_group FROM chats WHERE id = $1", chatID).Scan(&isGroup)
// 	if err != nil {
// 		http.Error(w, "Failed to get chat type", http.StatusInternalServerError)
// 		return
// 	}

// 	// Улучшенный запрос, который работает для обоих типов чатов
// 	// rows, err := db.Query(`
// 	//     SELECT
// 	//         m.id,
// 	//         m.content,
// 	//         m.created_at,
// 	//         m.user_id,
// 	//         m.is_system,
// 	//         m.parent_message_id,
// 	//         m.is_forwarded,
// 	//         m.original_sender_id,
// 	//         m.original_chat_id,
// 	//         u.username as sender_name,
// 	//         pm.content as parent_content,
// 	//         pu.username as parent_sender
// 	//     FROM messages m
// 	//     LEFT JOIN users u ON m.user_id = u.id
// 	//     LEFT JOIN messages pm ON m.parent_message_id = pm.id
// 	//     LEFT JOIN users pu ON pm.user_id = pu.id
// 	//     WHERE m.chat_id = $1
// 	//     ORDER BY m.created_at ASC
// 	// `, chatID)
// 	// Улучшенный запрос, который работает для обоих типов чатов
// 	rows, err := db.Query(`
//         SELECT
//             m.id,
//             m.content,
//             m.created_at,
//             m.user_id,
//             m.is_system,
//             m.parent_message_id,
//             m.is_forwarded,
//             m.original_sender_id,
//             m.original_chat_id,
//             u.username AS sender_name,
//             pm.content AS parent_content,
//             pu.username AS parent_sender,
//             ou.username AS original_sender_name
//         FROM messages m
//         LEFT JOIN users u ON m.user_id = u.id
//         LEFT JOIN messages pm ON m.parent_message_id = pm.id
//         LEFT JOIN users pu ON pm.user_id = pu.id
//         LEFT JOIN users ou ON m.original_sender_id = ou.id
//         WHERE m.chat_id = $1
//         ORDER BY m.created_at ASC
//     `, chatID)

// 	if err != nil {
// 		http.Error(w, "Query error: "+err.Error(), http.StatusInternalServerError)
// 		return
// 	}
// 	defer rows.Close()

// 	var messages []map[string]interface{}
// 	for rows.Next() {
// 		var (
// 			id              int
// 			content         string
// 			createdAt       time.Time
// 			userID          sql.NullInt64
// 			isSystem        bool
// 			parentMessageID sql.NullInt64
// 			isForwarded     bool
// 			originalSender  sql.NullInt64
// 			originalChat    sql.NullInt64
// 			senderName      sql.NullString
// 			parentContent   sql.NullString
// 			parentSender    sql.NullString
// 		)

// 		// if err := rows.Scan(&id, &content, &createdAt, &userID, &isSystem, &senderName, &senderImage); err != nil {
// 		// 	log.Println("Error scanning message row:", err)
// 		// 	continue
// 		// }
// 		if err := rows.Scan(
// 			&id,
// 			&content,
// 			&createdAt,
// 			&userID,
// 			&isSystem,
// 			&parentMessageID,
// 			&isForwarded,
// 			&originalSender,
// 			&originalChat,
// 			&senderName,
// 			&parentContent,
// 			&parentSender,
// 		); err != nil {
// 			log.Println("Error scanning message row:", err)
// 			continue
// 		}

// 		currentUserIDInt, _ := strconv.ParseInt(currentUserID, 10, 64)
// 		isMe := userID.Int64 == currentUserIDInt

// 		// messageData := map[string]interface{}{
// 		// 	"id":         id,
// 		// 	"text":       content,
// 		// 	"created_at": createdAt,
// 		// 	"user_id":    userID.Int64,
// 		// 	"is_system":  isSystem,
// 		// 	"isMe":       isMe,
// 		// 	"is_group":   isGroup, // Добавляем информацию о типе чата
// 		// 	"parent_message": parentContent,
// 		//     "parent_sender": parentSender,
// 		//     "is_forwarded":   isForwarded,
// 		//     "original_sender": originalSenderID,
// 		//     "original_chat":  originalChatID,
// 		// }
// 		// messageData := map[string]interface{}{
// 		// 	"id":              id,
// 		// 	"text":            content,
// 		// 	"created_at":      createdAt,
// 		// 	"user_id":         userID.Int64,
// 		// 	"is_system":       isSystem,
// 		// 	"isMe":            isMe,
// 		// 	"is_group":        isGroup,
// 		// 	"parent_message":  parentContent.String,
// 		// 	"parent_sender":   parentSender.String,
// 		// 	"is_forwarded":    isForwarded,
// 		// 	"original_sender": originalSender.Int64,
// 		// 	"original_chat":   originalChat.Int64,
// 		// }

// 		// if senderName.Valid {
// 		// 	messageData["sender_name"] = senderName.String
// 		// }
// 		// if len(senderImage) > 0 {
// 		// 	messageData["sender_image"] = senderImage
// 		// }
// 		var originalSenderName sql.NullString
//     if err := rows.Scan(&id, &content, &createdAt, &userID, &isSystem, &parentMessageID, &isForwarded, &originalSender, &originalChat, &senderName, &parentContent, &parentSender, &originalSenderName); err != nil {
//         log.Println("Error scanning message row:", err)
//         continue
//     }
//     messageData := map[string]interface{
//         "id": id,
//         "text": content,
//         "created_at": createdAt,
//         "user_id": userID.Int64,
//         "is_system": isSystem,
//         "isMe": isMe,
//         "is_group": isGroup,
//         "parent_message": parentContent.String,
//         "parent_sender": parentSender.String,
//         "is_forwarded": isForwarded,
//         "original_sender": originalSender.Int64,
//         "original_chat": originalChat.Int64,
//         "original_sender_name": originalSenderName.String,
//     }
//     if senderName.Valid {
//         messageData["sender_name"] = senderName.String
//     }

// 		messages = append(messages, messageData)
// 	}

// 	w.Header().Set("Content-Type", "application/json")
// 	log.Print(messages)
// 	json.NewEncoder(w).Encode(messages)
// }

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
	//log.Printf("Зpppp [%s]", response)

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(response)
}

func createGroupChatHandler(w http.ResponseWriter, r *http.Request) {
	// Парсим форму (включая файлы)
	err := r.ParseMultipartForm(10 << 20) // 10 MB
	if err != nil {
		http.Error(w, "Unable to parse form", http.StatusBadRequest)
		return
	}

	// Получаем данные из формы
	name := r.FormValue("name")
	description := r.FormValue("description")
	createdBy := r.FormValue("created_by")
	isGroup := r.FormValue("is_group") == "true"

	// Получаем user_ids как строку, разделенную запятыми
	userIDsStr := r.FormValue("user_ids")

	log.Println("Received user_ids:", userIDsStr) // Логируем полученные данные

	// Разделяем строку на отдельные значения
	userIDs := strings.Split(userIDsStr, ",")
	// Добавляем создателя в список участников
	userIDs = append(userIDs, createdBy)
	if len(userIDs) == 0 {
		http.Error(w, "No user IDs provided", http.StatusBadRequest)
		return
	}

	// Обработка изображения (только для группового чата)
	var imageBytes []byte
	if isGroup {
		file, _, err := r.FormFile("image")
		if err == nil {
			defer file.Close()
			imageBytes, err = io.ReadAll(file)
			if err != nil {
				http.Error(w, "Error reading image", http.StatusBadRequest)
				return
			}
		}
	}

	// Подключаемся к базе данных
	db, err := connectDB()
	if err != nil {
		http.Error(w, "Database connection failed", http.StatusInternalServerError)
		return
	}
	defer db.Close()

	// Начинаем транзакцию
	tx, err := db.Begin()
	if err != nil {
		http.Error(w, "Transaction failed to start", http.StatusInternalServerError)
		return
	}

	// Создаем запись в таблице chats
	var chatID int
	err = tx.QueryRow("INSERT INTO chats (is_group) VALUES ($1) RETURNING id", isGroup).Scan(&chatID)
	if err != nil {
		tx.Rollback()
		http.Error(w, "Failed to create chat: "+err.Error(), http.StatusInternalServerError)
		return
	}

	// Если это групповой чат, создаем запись в таблице group_chats
	if isGroup {
		_, err = tx.Exec(
			"INSERT INTO group_chats (chat_id, name, description, created_by, image) VALUES ($1, $2, $3, $4, $5)",
			chatID, name, description, createdBy, imageBytes,
		)
		if err != nil {
			tx.Rollback()
			http.Error(w, "Failed to create group chat: "+err.Error(), http.StatusInternalServerError)
			return
		}
	}

	// Добавляем участников в таблицу participants
	for _, userID := range userIDs {
		isAdmin := userID == createdBy // Создатель чата становится администратором
		_, err = tx.Exec(
			"INSERT INTO participants (chat_id, user_id, is_admin) VALUES ($1, $2, $3)",
			chatID, userID, isAdmin,
		)
		if err != nil {
			tx.Rollback()
			http.Error(w, "Failed to add participant: "+err.Error(), http.StatusInternalServerError)
			return
		}
	}

	// Добавляем системное сообщение о создании чата
	var messageContent string
	if isGroup {
		messageContent = "Групповой чат создан"
	} else {
		messageContent = "Чат создан"
	}

	_, err = tx.Exec(
		"INSERT INTO messages (chat_id, content, is_system) VALUES ($1, $2, true)",
		chatID, messageContent,
	)
	if err != nil {
		tx.Rollback()
		http.Error(w, "Failed to create system message: "+err.Error(), http.StatusInternalServerError)
		return
	}

	// Завершаем транзакцию
	err = tx.Commit()
	if err != nil {
		http.Error(w, "Transaction commit failed: "+err.Error(), http.StatusInternalServerError)
		return
	}

	// Возвращаем успешный ответ
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]interface{}{
		"chat_id":  chatID,
		"is_group": isGroup,
	})
}

func allUsersHandler(w http.ResponseWriter, r *http.Request) {
	if r.Method != "GET" {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}

	currentUserID := r.URL.Query().Get("current_user_id")
	if currentUserID == "" {
		http.Error(w, "Current user ID is required", http.StatusBadRequest)
		return
	}

	db, err := connectDB()
	if err != nil {
		http.Error(w, "Database error", http.StatusInternalServerError)
		return
	}
	defer db.Close()

	// Запрос всех пользователей, исключая текущего
	rows, err := db.Query(`
        SELECT id, username 
        FROM users 
        WHERE id != $1
    `, currentUserID)

	if err != nil {
		http.Error(w, "Query error: "+err.Error(), http.StatusInternalServerError)
		return
	}
	defer rows.Close()

	var users []map[string]interface{}
	for rows.Next() {
		var id int
		var username string
		if err := rows.Scan(&id, &username); err != nil {
			log.Println("Error scanning user row:", err)
			continue
		}
		users = append(users, map[string]interface{}{"id": id, "username": username})
	}

	w.Header().Set("Content-Type", "application/json")
	if users == nil {
		users = []map[string]interface{}{}
	}
	json.NewEncoder(w).Encode(users)
}

func forwardMessage(w http.ResponseWriter, r *http.Request) {
	var data struct {
		ChatID         int    `json:"chat_id"`
		UserID         int    `json:"user_id"`
		Text           string `json:"text"`
		OriginalSender *int   `json:"original_sender_id"` // Изменяем на указатель
		OriginalChat   *int   `json:"original_chat_id"`   // Изменяем на указатель
	}
	if err := json.NewDecoder(r.Body).Decode(&data); err != nil {
		log.Printf("Ошибка декодирования тела запроса: %v", err)
		http.Error(w, "Invalid request body", http.StatusBadRequest)
		return
	}
	log.Printf("Получены данные для пересылки: %+v", data)

	if data.ChatID == 0 || data.UserID == 0 {
		log.Printf("Отсутствуют обязательные поля: chat_id или user_id")
		http.Error(w, "Missing required fields", http.StatusBadRequest)
		return
	}

	db, err := connectDB()
	if err != nil {
		log.Printf("Ошибка подключения к БД: %v", err)
		http.Error(w, "Database error", http.StatusInternalServerError)
		return
	}
	defer db.Close()

	var originalSender sql.NullInt64
	if data.OriginalSender != nil {
		originalSender.Int64 = int64(*data.OriginalSender)
		originalSender.Valid = true
	}
	var originalChat sql.NullInt64
	if data.OriginalChat != nil {
		originalChat.Int64 = int64(*data.OriginalChat)
		originalChat.Valid = true
	}

	var messageID int
	err = db.QueryRow(`
	  INSERT INTO messages (chat_id, user_id, content, is_forwarded, original_sender_id, original_chat_id)
	  VALUES ($1, $2, $3, true, $4, $5) RETURNING id
	`, data.ChatID, data.UserID, data.Text, originalSender, originalChat).Scan(&messageID)
	if err != nil {
		log.Printf("Ошибка вставки сообщения в БД: %v", err)
		http.Error(w, "Failed to insert message", http.StatusInternalServerError)
		return
	}
	log.Printf("Сообщение успешно вставлено с ID: %d", messageID)

	var originalSenderName string
	if originalSender.Valid {
		err = db.QueryRow("SELECT username FROM users WHERE id = $1", originalSender.Int64).Scan(&originalSenderName)
		if err != nil {
			log.Printf("Ошибка получения имени оригинального отправителя: %v", err)
			originalSenderName = "Unknown"
		}
	} else {
		originalSenderName = ""
	}
	log.Printf("Имя оригинального отправителя: %s", originalSenderName)

	message := map[string]interface{}{
		"id":           messageID,
		"chat_id":      data.ChatID,
		"user_id":      data.UserID,
		"text":         data.Text,
		"created_at":   time.Now().Format(time.RFC3339),
		"is_forwarded": true,
	}
	if originalSender.Valid {
		message["original_sender_id"] = originalSender.Int64
		message["original_sender_name"] = originalSenderName
	}
	if originalChat.Valid {
		message["original_chat_id"] = originalChat.Int64
	}

	clientsMu.Lock()
	log.Printf("Рассылка пересланного сообщения клиентам в чате %d", data.ChatID)
	for client, info := range clients {
		if info.chatID == data.ChatID {
			err := client.WriteJSON(message)
			if err != nil {
				log.Printf("Ошибка отправки сообщения клиенту: %v", err)
				client.Close()
				delete(clients, client)
			}
		}
	}
	clientsMu.Unlock()

	log.Printf("Пересылка сообщения успешно завершена")
	w.WriteHeader(http.StatusOK)
	w.Write([]byte("Message forwarded successfully"))
}

func resetUnreadHandler(w http.ResponseWriter, r *http.Request) {
	var data struct {
		ChatID int `json:"chat_id"`
		UserID int `json:"user_id"`
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
		"UPDATE participants SET unread_count = 0 WHERE chat_id = $1 AND user_id = $2",
		data.ChatID, data.UserID,
	)
	if err != nil {
		http.Error(w, "Failed to reset unread count", http.StatusInternalServerError)
		return
	}

	w.WriteHeader(http.StatusOK)
	w.Write([]byte("Unread count reset"))
}

// Обработчик для получения количества участников группового чата
func getGroupParticipantsCountHandler(w http.ResponseWriter, r *http.Request) {
	chatIDStr := r.URL.Query().Get("chat_id")
	chatID, err := strconv.Atoi(chatIDStr)
	if err != nil {
		log.Printf("Невалидный chat_id в запросе: %s", chatIDStr)
		http.Error(w, "Invalid chat_id", http.StatusBadRequest)
		return
	}

	db, err := connectDB()
	if err != nil {
		log.Printf("Ошибка подключения к БД: %v", err)
		http.Error(w, "Database error", http.StatusInternalServerError)
		return
	}
	defer db.Close()

	// Проверяем, что чат групповой
	var isGroup bool
	err = db.QueryRow("SELECT is_group FROM chats WHERE id = $1", chatID).Scan(&isGroup)
	if err != nil {
		log.Printf("Ошибка проверки типа чата %d: %v", chatID, err)
		http.Error(w, "Chat not found", http.StatusNotFound)
		return
	}
	if !isGroup {
		http.Error(w, "Not a group chat", http.StatusBadRequest)
		return
	}

	// Получаем общее количество участников
	var participantsCount int
	err = db.QueryRow("SELECT COUNT(*) FROM participants WHERE chat_id = $1", chatID).Scan(&participantsCount)
	if err != nil {
		log.Printf("Ошибка подсчета участников чата %d: %v", chatID, err)
		http.Error(w, "Failed to count participants", http.StatusInternalServerError)
		return
	}

	// Формируем ответ
	response := map[string]interface{}{
		"chat_id":            chatID,
		"participants_count": participantsCount,
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(response)
}
