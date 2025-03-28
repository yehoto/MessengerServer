package main

import (
	"database/sql"
	"encoding/json"
	"log"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"time"

	"encoding/base64"

	"github.com/gorilla/websocket"

	"io"
	//"database/sql"
	//"github.com/gorilla/websocket"
)

// Настройка WebSocket апгрейдера с разрешением всех Origin (только для разработки!)
// websocket.Upgrader - структура из пакета gorilla/websocket, которая:
// Конвертирует обычное HTTP-соединение в WebSocket
// Настраивает параметры "рукопожатия" (handshake)
// Контролирует политики безопасности

// Origin - HTTP-заголовок, который браузеры автоматически добавляют к WebSocket-запросам
// Указывает домен, с которого пришел запрос (например: https://my-site.com)
var upgrader = websocket.Upgrader{
	CheckOrigin: func(r *http.Request) bool {
		log.Printf("Разрешен запрос от origin: %s", r.Header.Get("Origin"))
		return true
	},
}

type clientInfo struct {
	userID int
	chatID int
}

var clients = make(map[*websocket.Conn]clientInfo)

// Хранилище подключенных клиентов и мьютекс для безопасности
// var clients = make(map[*websocket.Conn]bool) //bool - соединение активно/нет, но в реализации особо не нужно
// Мьютекс — это примитив синхронизации, который позволяет:
// Блокировать доступ к данным, если их использует другая горутина.
// Разблокировать доступ, когда работа с данными завершена.
var clientsMu sync.Mutex

// Хранилище статусов пользователей и мьютекс
var (
	userStatuses = make(map[int]bool)
	userStatusMu sync.Mutex
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
	statusMessage := map[string]interface{}{ //Тип interface{} в Go используется для создания переменных, которые могут хранить значения любого типа.
		"type":    "user_status",
		"user_id": userID,
		"online":  online,
	}

	clientsMu.Lock()
	defer clientsMu.Unlock()
	log.Printf("Рассылка статуса пользователя %d (%t) для %d клиентов", userID, online, len(clients))

	for client := range clients {
		err := client.WriteJSON(statusMessage)
		if err != nil {
			log.Printf("Ошибка отправки [%d]: %v", userID, err)
			client.Close()
			delete(clients, client)
		}
	}
}

// Обработчик WebSocket соединений
func handleWebSocket(w http.ResponseWriter, r *http.Request) {
	conn, err := upgrader.Upgrade(w, r, nil)
	if err != nil {
		log.Printf("Ошибка апгрейда WebSocket: %v", err)
		return
	}
	defer conn.Close()

	userIDStr := r.URL.Query().Get("user_id")
	userID, _ := strconv.Atoi(userIDStr)
	chatIDStr := r.URL.Query().Get("chat_id")
	chatID, _ := strconv.Atoi(chatIDStr)
	log.Printf("Подключение WebSocket: user_id=%d, chat_id=%d", userID, chatID)

	clientsMu.Lock()
	clients[conn] = clientInfo{userID: userID, chatID: chatID}
	clientsMu.Unlock()

	updateUserStatus(userID, true)
	broadcastUserStatus(userID, true)

	for {
		_, message, err := conn.ReadMessage()
		if err != nil {
			log.Printf("Ошибка чтения сообщения WebSocket: %v", err)
			break
		}
		log.Printf("Получено сообщение через WebSocket: %s", string(message))

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

		db, err := connectDB()
		if err != nil {
			log.Printf("Ошибка подключения к БД в WebSocket: %v", err)
			continue
		}

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

		var senderName string
		err = db.QueryRow("SELECT username FROM users WHERE id = $1", msgData.UserID).Scan(&senderName)
		if err != nil {
			log.Printf("Ошибка получения имени отправителя: %v", err)
			senderName = "Unknown"
		}

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
		if msgData.ParentMessageID != nil {
			var parentContent string
			err := db.QueryRow("SELECT content FROM messages WHERE id = $1", *msgData.ParentMessageID).Scan(&parentContent)
			if err == nil {
				msgDataMap["parent_content"] = parentContent
			} else {
				log.Printf("Ошибка получения parent_content: %v", err)
			}
		}

		// Получаем список участников чата
		rows, err := db.Query("SELECT user_id FROM participants WHERE chat_id = $1", msgData.ChatID)
		if err != nil {
			log.Printf("Ошибка получения участников чата: %v", err)
			db.Close()
			continue
		}
		defer rows.Close()

		var participantIDs []int
		for rows.Next() {
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

// Отправка статуса конкретному клиенту
func sendUserStatus(conn *websocket.Conn, userID int, online bool) {
	statusMessage := map[string]interface{}{
		"type":    "user_status",
		"user_id": userID,
		"online":  online,
	}

	log.Printf("Отправка статуса [%d:%t] клиенту [%v]", userID, online, conn.RemoteAddr())
	err := conn.WriteJSON(statusMessage)
	if err != nil {
		log.Printf("Ошибка отправки статуса [%d]: %v", userID, err)
		conn.Close()
		delete(clients, conn)
	}
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
