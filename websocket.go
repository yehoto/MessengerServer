package main

import (
	"database/sql"
	"encoding/base64"
	"encoding/json"
	"io"
	"log"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/gorilla/websocket"
)

var upgrader = websocket.Upgrader{
	ReadBufferSize:  1024,
	WriteBufferSize: 1024,
	// Явно разрешаем все origin
	CheckOrigin: func(r *http.Request) bool { return true },
}

// Структура для хранения информации о клиенте WebSocket
type clientInfo struct {
	userID int
	chatID int
}

var clients = make(map[*websocket.Conn]clientInfo) // Карта подключенных клиентов

var clientsMu sync.Mutex // Мьютекс для безопасного доступа к карте clients

// Хранилище статусов пользователей и мьютекс
var (
	userStatuses = make(map[int]bool)
	userStatusMu sync.Mutex // Мьютекс для синхронизации доступа
)

// Обновление статуса пользователя с логированием
func updateUserStatus(userID int, online bool) {
	userStatusMu.Lock()         //блокируем доступ к мапе для другоих горутин
	defer userStatusMu.Unlock() //defer откладывает выполнение указанной функции до момента, когда текущая функция завершится
	prevStatus := userStatuses[userID]
	userStatuses[userID] = online
	log.Printf("Статус пользователя %d изменен: %t -> %t", userID, prevStatus, online)
}

// Рассылка статуса пользователя всем клиентам
func broadcastUserStatus(userID int, online bool) {
	// Формируем сообщение о статусе
	statusMessage := map[string]interface{}{
		"type":    "user_status",
		"user_id": userID,
		"online":  online,
	}

	clientsMu.Lock()
	defer clientsMu.Unlock()
	log.Printf("Рассылка статуса пользователя %d (%t) для %d клиентов", userID, online, len(clients))
	// Отправляем сообщение всем подключенным клиентам
	for client := range clients {
		err := client.WriteJSON(statusMessage)
		if err != nil {
			// При ошибке закрываем соединение и удаляем клиента
			log.Printf("Ошибка отправки [%d]: %v", userID, err)
			client.Close()
			delete(clients, client)
		}
	}
}

// Обработчик WebSocket соединений
func handleWebSocket(w http.ResponseWriter, r *http.Request) {
	// Обновляем HTTP соединение до WebSocket
	conn, err := upgrader.Upgrade(w, r, nil)
	if err != nil {
		log.Printf("Ошибка апгрейда WebSocket: %v", err)
		return
	}
	defer conn.Close() // Гарантированное закрытие соединения при выходе
	// Получаем параметры из URL
	userIDStr := r.URL.Query().Get("user_id")
	userID, _ := strconv.Atoi(userIDStr)
	chatIDStr := r.URL.Query().Get("chat_id")
	chatID, _ := strconv.Atoi(chatIDStr)
	log.Printf("Подключение WebSocket: user_id=%d, chat_id=%d", userID, chatID)
	// Регистрируем нового клиента
	clientsMu.Lock()
	clients[conn] = clientInfo{userID: userID, chatID: chatID}
	clientsMu.Unlock()
	// Обновляем статус пользователя
	updateUserStatus(userID, true)
	broadcastUserStatus(userID, true)

	// Бесконечный цикл обработки входящих сообщений
	for {
		_, message, err := conn.ReadMessage()
		if err != nil {
			log.Printf("Ошибка чтения сообщения WebSocket: %v", err)
			break
		}
		log.Printf("Получено сообщение через WebSocket: %s", string(message))

		// Пытаемся распарсить сообщение как команду
		var command struct {
			Type      string `json:"type"`
			MessageID int    `json:"message_id"`
			UserID    int    `json:"user_id"`
			NewText   string `json:"new_text"`
		}

		if err := json.Unmarshal(message, &command); err == nil && command.Type != "" {
			switch command.Type {
			case "delete_for_me":
				handleDeleteForMeCommand(conn, command.MessageID, command.UserID)
				continue
			case "delete_for_everyone":
				handleDeleteForEveryoneCommand(conn, command.MessageID, command.UserID)
				continue
			case "edit_message":
				handleEditMessageCommand(conn, command.MessageID, command.UserID, command.NewText)
				continue
			}
		}

		// Если не команда, обрабатываем как обычное сообщение
		var msgData struct {
			ChatID           int    `json:"chat_id"`
			UserID           int    `json:"user_id"`
			Text             string `json:"text"`
			ParentMessageID  *int   `json:"parent_message_id"`
			IsForwarded      bool   `json:"is_forwarded"`
			OriginalSenderID *int   `json:"original_sender_id"`
			OriginalChatID   *int   `json:"original_chat_id"`
		}
		if err := json.Unmarshal(message, &msgData); err != nil {
			log.Printf("Ошибка парсинга сообщения WebSocket: %v", err)
			continue
		}

		// Подключаемся к базе данных
		db, err := connectDB()
		if err != nil {
			log.Printf("Ошибка подключения к БД в WebSocket: %v", err)
			continue
		}
		// Подготовка данных для SQL-запроса
		var parentMessageID sql.NullInt64
		if msgData.ParentMessageID != nil {
			parentMessageID.Int64 = int64(*msgData.ParentMessageID)
			parentMessageID.Valid = true
		}
		var originalSenderID sql.NullInt64
		if msgData.OriginalSenderID != nil {
			originalSenderID.Int64 = int64(*msgData.OriginalSenderID)
			originalSenderID.Valid = true
		}
		var originalChatID sql.NullInt64
		if msgData.OriginalChatID != nil {
			originalChatID.Int64 = int64(*msgData.OriginalChatID)
			originalChatID.Valid = true
		}

		// Сохраняем сообщение в базу данных
		var messageID int
		err = db.QueryRow(
			"INSERT INTO messages (chat_id, user_id, content, parent_message_id, is_forwarded, original_sender_id, original_chat_id) VALUES ($1, $2, $3, $4, $5, $6, $7) RETURNING id",
			msgData.ChatID, msgData.UserID, msgData.Text, parentMessageID, msgData.IsForwarded, originalSenderID, originalChatID,
		).Scan(&messageID)
		if err != nil {
			log.Printf("Ошибка сохранения сообщения в БД: %v", err)
			db.Close()
			continue
		}
		// Получаем имя отправителя
		var senderName string
		err = db.QueryRow("SELECT username FROM users WHERE id = $1", msgData.UserID).Scan(&senderName)
		if err != nil {
			log.Printf("Ошибка получения имени отправителя: %v", err)
			senderName = "Unknown"
		}
		// Формируем объект сообщения для рассылки
		msgDataMap := map[string]interface{}{
			"id":                 messageID,
			"chat_id":            msgData.ChatID,
			"user_id":            msgData.UserID,
			"text":               msgData.Text,
			"created_at":         time.Now().Format(time.RFC3339),
			"isMe":               false,
			"sender_name":        senderName,
			"parent_message_id":  msgData.ParentMessageID,
			"is_forwarded":       msgData.IsForwarded,
			"original_sender_id": msgData.OriginalSenderID,
			"original_chat_id":   msgData.OriginalChatID,
		}
		// Если есть родительское сообщение, получаем его текст
		if msgData.ParentMessageID != nil {
			var parentContent string
			err := db.QueryRow("SELECT content FROM messages WHERE id = $1", *msgData.ParentMessageID).Scan(&parentContent)
			if err == nil {
				msgDataMap["parent_content"] = parentContent
			} else {
				log.Printf("Ошибка получения parent_content: %v", err)
			}
		}

		// Получаем участников чата из базы данных
		rows, err := db.Query("SELECT user_id FROM participants WHERE chat_id = $1", msgData.ChatID)
		if err != nil {
			log.Printf("Ошибка получения участников чата: %v", err)
			db.Close()
			continue
		}
		defer rows.Close()

		var participantIDs []int // Создаем пустой срез для хранения ID участников чата
		for rows.Next() {        // Итерируем по строкам результата SQL-запрос
			var participantID int
			if err := rows.Scan(&participantID); err != nil {
				log.Printf("Ошибка сканирования participant_id: %v", err)
				continue
			}
			participantIDs = append(participantIDs, participantID)
		}
		db.Close()

		// Рассылка всем участникам чата
		clientsMu.Lock()
		log.Printf("Рассылка сообщения клиентам в чате %d (участники: %v)", msgData.ChatID, participantIDs)
		for client, info := range clients {
			// Проверяем, является ли клиент участником чата
			for _, pid := range participantIDs {
				if info.userID == pid {
					isMe := (client == conn)
					msgDataMap["isMe"] = isMe
					messageWithIsMe, _ := json.Marshal(msgDataMap)
					err := client.WriteMessage(websocket.TextMessage, messageWithIsMe)
					if err != nil {
						log.Printf("Ошибка отправки сообщения клиенту (user_id=%d): %v", info.userID, err)
						client.Close()
						delete(clients, client)
					}
					break // Прерываем внутренний цикл, так как сообщение уже отправлено этому клиенту
				}
			}
		}
		clientsMu.Unlock()
	}

	log.Printf("Клиент отключен: user_id=%d", userID)
	updateUserStatus(userID, false)
	broadcastUserStatus(userID, false)
	// Удаляем клиента из списка
	clientsMu.Lock()
	delete(clients, conn)
	clientsMu.Unlock()
}

// HTTP обработчик для проверки статуса
func getUserStatusHandler(w http.ResponseWriter, r *http.Request) {
	userIDStr := r.URL.Query().Get("user_id")
	userID, err := strconv.Atoi(userIDStr)
	if err != nil {
		log.Printf("Невалидный user_id в запросе: %s", userIDStr)
		http.Error(w, "Invalid user_id", http.StatusBadRequest)
		return
	}

	userStatusMu.Lock()
	online := userStatuses[userID]
	userStatusMu.Unlock()

	log.Printf("Запрос статуса [%d]: %t", userID, online)
	json.NewEncoder(w).Encode(map[string]bool{"online": online})
}

// Функция для рассылки уведомления о создании группы
func broadcastNewGroup(chatID int, chatName string, userIDs []int, groupImage string) {
	newGroupMessage := map[string]interface{}{
		"type":        "new_group",
		"chat_id":     chatID,
		"chat_name":   chatName,
		"is_group":    true,
		"user_ids":    userIDs,
		"group_image": groupImage, // Передаем изображение как base64 или другой формат
	}

	clientsMu.Lock()
	defer clientsMu.Unlock()
	log.Printf("Рассылка уведомления о новой группе %d участникам: %v", chatID, userIDs)

	for client, info := range clients {
		// Отправляем уведомление только участникам группы
		for _, userID := range userIDs {
			if info.userID == userID {
				err := client.WriteJSON(newGroupMessage)
				if err != nil {
					log.Printf("Ошибка отправки уведомления о группе клиенту [%d]: %v", info.userID, err)
					client.Close()
					delete(clients, client)
				}
				break
			}
		}
	}
}

func createGroupChatHandler(w http.ResponseWriter, r *http.Request) {
	// Парсинг формы с поддержкой файлов (10 МБ - лимит)
	err := r.ParseMultipartForm(10 << 20) // 10 << 20 = 10 * 2^20 = 10,485,760 байт
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
	// createGroupChatHandler
	err = tx.Commit()
	if err != nil {
		http.Error(w, "Transaction commit failed: "+err.Error(), http.StatusInternalServerError)
		return
	}

	// Собираем список userID в формате int
	var userIDInts []int
	for _, userID := range userIDs {
		id, _ := strconv.Atoi(userID) // Преобразуем string в int
		userIDInts = append(userIDInts, id)
	}

	// Отправляем уведомление о новом групповом чате
	if isGroup {
		groupImageStr := ""
		if len(imageBytes) > 0 {
			groupImageStr = base64.StdEncoding.EncodeToString(imageBytes) // Передаем изображение как base64
		}
		broadcastNewGroup(chatID, name, userIDInts, groupImageStr)
	}

	// Возвращаем успешный ответ
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]interface{}{
		"chat_id":  chatID,
		"is_group": isGroup,
	})
}

func broadcastNewChat(chatID int, userIDs []int) {
	newChatMessage := map[string]interface{}{
		"type":     "new_chat",
		"chat_id":  chatID,
		"is_group": false,
		"user_ids": userIDs,
	}

	clientsMu.Lock()
	defer clientsMu.Unlock()
	log.Printf("Рассылка уведомления о новом чате %d участникам: %v", chatID, userIDs)

	for client, info := range clients {
		for _, userID := range userIDs {
			if info.userID == userID {
				err := client.WriteJSON(newChatMessage)
				if err != nil {
					log.Printf("Ошибка отправки уведомления клиенту [%d]: %v", info.userID, err)
					client.Close()
					delete(clients, client)
				}
				break
			}
		}
	}
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

func createChatHandler(w http.ResponseWriter, r *http.Request) {
	// Получаем ID текущего пользователя и собеседника из параметров запроса
	currentUserIDStr := r.FormValue("current_user_id")
	targetUserIDStr := r.FormValue("user_id")
	// Получение параметров из формы
	currentUserID, err := strconv.Atoi(currentUserIDStr)
	if err != nil {
		http.Error(w, "Invalid current_user_id", http.StatusBadRequest)
		return
	}
	targetUserID, err := strconv.Atoi(targetUserIDStr)
	if err != nil {
		http.Error(w, "Invalid user_id", http.StatusBadRequest)
		return
	}

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
	// Создание чата (is_group=false по умолчанию)
	var chatID int
	if err := tx.QueryRow("INSERT INTO chats DEFAULT VALUES RETURNING id").Scan(&chatID); err != nil {
		tx.Rollback()
		http.Error(w, "Chat creation failed", http.StatusInternalServerError)
		return
	}
	// Добавление участников
	if _, err := tx.Exec("INSERT INTO participants (chat_id, user_id) VALUES ($1, $2), ($1, $3)",
		chatID, currentUserID, targetUserID); err != nil {
		tx.Rollback()
		http.Error(w, "Participants error", http.StatusInternalServerError)
		return
	}
	// Системное сообщение о создании
	if _, err := tx.Exec("INSERT INTO messages (chat_id, content, is_system) VALUES ($1, $2, true)",
		chatID, "Чат создан"); err != nil {
		tx.Rollback()
		http.Error(w, "System message error", http.StatusInternalServerError)
		return
	}

	if err := tx.Commit(); err != nil {
		http.Error(w, "Transaction commit failed", http.StatusInternalServerError)
		return
	}

	// Собираем список участников для уведомления
	userIDs := []int{currentUserID, targetUserID}

	// Отправляем уведомление о новом чате через WebSocket
	broadcastNewChat(chatID, userIDs)

	// Возвращаем успешный ответ
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]int{"chatId": chatID})
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

	// Сложный SQL-запрос для получения чатов:
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
		// Формирование объекта чата
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

func broadcastMessageDeletion(messageID int) {
	deletionMsg := map[string]interface{}{
		"type": "message_deleted",
		"id":   messageID,
	}

	clientsMu.Lock()
	defer clientsMu.Unlock()

	for client := range clients {
		err := client.WriteJSON(deletionMsg)
		if err != nil {
			log.Printf("Broadcast deletion error: %v", err)
			client.Close()
			delete(clients, client)
		}
	}
}

func broadcastMessageEdit(chatID, messageID int, newText string, editedAt time.Time) {
	editMsg := map[string]interface{}{
		"type":      "message_edited",
		"id":        messageID,
		"chat_id":   chatID,
		"new_text":  newText,
		"edited_at": editedAt.Format(time.RFC3339),
	}

	clientsMu.Lock()
	defer clientsMu.Unlock()
	log.Printf("Broadcasting message edit: %+v", editMsg)

	for client, info := range clients {
		// Отправляем только участникам этого чата
		if info.chatID == chatID {
			log.Printf("Sending edit notification to user %d in chat %d", info.userID, chatID)
			err := client.WriteJSON(editMsg)
			if err != nil {
				log.Printf("Broadcast edit error to user %d: %v", info.userID, err)
				client.Close()
				delete(clients, client)
			}
		}
	}
}

func handleDeleteForEveryoneCommand(conn *websocket.Conn, messageID, userID int) {
	db, err := connectDB()
	if err != nil {
		log.Printf("DB error: %v", err)
		return
	}
	defer db.Close()

	// Проверяем, что пользователь - автор сообщения
	var authorID int
	err = db.QueryRow("SELECT user_id FROM messages WHERE id = $1", messageID).Scan(&authorID)
	if err != nil || authorID != userID {
		log.Printf("Unauthorized delete attempt: user %d tried to delete message %d", userID, messageID)
		return
	}

	// Обновление сообщения в БД
	_, err = db.Exec(`
        UPDATE messages 
        SET is_deleted = TRUE, 
            content = 'Сообщение удалено'
        WHERE id = $1`,
		messageID)

	if err != nil {
		log.Printf("Delete for everyone error: %v", err)
		return
	}

	// Рассылаем уведомление об удалении
	broadcastMessageDeletion(messageID)
}

func handleEditMessageCommand(conn *websocket.Conn, messageID, userID int, newText string) {
	db, err := connectDB()
	if err != nil {
		log.Printf("DB connection error: %v", err)
		return
	}
	defer db.Close()

	var chatID int
	var authorID int
	var editedAt time.Time

	// Проверяем авторство и получаем chat_id
	err = db.QueryRow(`
        SELECT chat_id, user_id 
        FROM messages 
        WHERE id = $1`,
		messageID,
	).Scan(&chatID, &authorID)

	if err != nil || authorID != userID {
		log.Printf("Unauthorized edit: user %d, message %d", userID, messageID)
		return
	}

	// Обновляем сообщение
	err = db.QueryRow(`
        UPDATE messages 
        SET content = $1, 
            is_edited = TRUE,
            edited_at = CURRENT_TIMESTAMP
        WHERE id = $2
        RETURNING edited_at`,
		newText, messageID,
	).Scan(&editedAt)

	if err != nil {
		log.Printf("Edit error: %v", err)
		return
	}

	broadcastMessageEdit(chatID, messageID, newText, editedAt)
}
func handleDeleteForMeCommand(conn *websocket.Conn, messageID, userID int) {
	db, err := connectDB()
	if err != nil {
		log.Printf("DB error: %v", err)
		return
	}
	defer db.Close()

	// Помечаем сообщение как удаленное для этого пользователя
	_, err = db.Exec(`
        INSERT INTO deleted_messages (message_id, user_id)
        VALUES ($1, $2) 
        ON CONFLICT (message_id, user_id) DO UPDATE SET deleted = true`,
		messageID, userID)

	if err != nil {
		log.Printf("Delete for me error: %v", err)
		return
	}

	// Отправляем подтверждение только этому клиенту
	confirmMsg := map[string]interface{}{
		"type":           "message_deleted_for_me",
		"id":             messageID,
		"deleted_for_me": true,
	}

	conn.WriteJSON(confirmMsg)
}
